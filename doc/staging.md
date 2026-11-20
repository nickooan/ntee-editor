# internal/staging

**Introduction**

Agent edits that wait in the editor instead of landing in the files. A coding agent sends an edit session's edits over the socket (`ntee --msg --msg-edit-session <id> write|replace …`); this package keeps them on a staged copy of each file, so the files on disk stay untouched while the agent works. When the agent ends the session, the editor either applies them all at once or opens them for the user to review first; that choice is the user's setting (Ctrl+T › agent edits) and lives in `internal/app`. Agents that speak MCP reach the same commands as tools through `ntee --mcp` ([mcpserver.md](mcpserver.md)). Any agent can read a file's latest content through the editor — staged edits included — and edits to a file another session staged build on them (Layers, below). Applying never overwrites a file that changed on disk after it was staged.

Every session is one ntee-db record (`store.AgentEditSession`): each file's content before and after, and each edit as the agent sent it. The record is saved after every edit and stays after the session finishes, so which file contents an agent changed — and what became of them — can be traced later.

**Architecture**

`Registry` holds one workspace's edit sessions: the pending ones (`stagedSet`, by session id) and, for this run, the finished ones without their contents (so a late `wait` hears the outcome and a finished id is not reused). Edits arrive on the socket's connection goroutines; the UI reads snapshots.

Concurrency rule: a pending session's record is copy-on-write. An edit takes the set's `saveMu` (so edits and saves of one session run in order), builds the next record from a snapshot, saves it to the log, and only then publishes it under the short `mu`. A failed save leaves the session exactly as it was, and a reader never waits on a db write — it only takes `mu` to copy the record header, sharing slices that are never mutated in place.

A session starts on its first edit (a refused first edit leaves none behind) and takes edits until the agent's end (`MarkAgentEnded`). It then waits for `Finish` (apply results recorded) or `Discard`, both of which move it to the log's finished records, trimmed to the newest `keep`.

A file's base is read on its first edit, through the workspace jail (`filetree.ReadContainedFile`). A missing file is a new file. Binary files and files over `MaxFileBytes` (2 MiB, the review page's limit) are refused. Edits stack: a replace works on the content earlier edits staged.

**Layers.** Several sessions may stage the same file, and they stack: a session staging a file another pending session already staged builds on that session's staged copy, not the disk. Its `BaseContent` is that copy and `BaseSession` names the session (`""` = the disk). So every agent sees, and edits, the latest content — not only the one that staged it.

- **The top layer** (`Latest`) is found by walking up from the disk. The root is a layer whose base is the disk as it is now: based on no session, or on one no longer pending (an applied base wrote its content to disk, so its upper layer is now the root; a discarded one did not, so its upper layer is an orphan and skipped). Each next layer is the one built on the layer below. The newest root wins a tie (sessions come oldest first).
- **First stagings are serialized** by the registry's `layerMu`, held from reading the top layer until the record is published, so two sessions never both build on the same layer. Lock order: a set's `saveMu`, then `layerMu` — never the other way.
- **Applying** a file checks the whole chain (`ApplyCheck`): each layer below must still hold the content the one above built on, and the bottom's base must still be the disk. The top content is written once; the lower layers' copies are then *settled* (`SettleFile`): marked applied ("written with edit session B") in their still-pending sessions, so their own apply leaves the file alone and a later edit to it is refused. A file built on a session that changed it again, or that was discarded, conflicts and is not written.

**Functions**

### staging.go

- `Write` — stages a file's whole new content. The edit's line stats (`LineChanges` against the previous staged content) go into the record and the agent's reply.
- `Replace` — stages an exact-text replacement. The old text must match exactly once (`matchOffsets`, non-overlapping), or every match with `All`. With several matches and no `All`, a `HintLine` picks the match covering that line, else the nearest, the earlier on a tie (`pickMatch`); without one the agent is told how many matches there are and on which lines. The reply carries the changed lines in the new content.
- `edit` — the shared edit path: snapshot, apply, save, publish, as described under Architecture.
- `Content` — a file's staged content in the session, or its latest content (`Latest`) when the session has not staged it, with the session it came from. This is what `show` and `read` print, since the agent's own file reads see the disk.
- `MarkAgentEnded`, `Finish`, `Discard` — the end of a session. `Finish` sets each file's `Result` from the apply results and derives the status (`applied`, `partly-applied`, `not-applied`) and an outcome sentence naming each file not written and why.
- `Registry.ApplyFiles` — writes a session's staged files to disk, run off the UI goroutine. A file is written only if `ApplyCheck` allows it; otherwise it is `conflicted` (or skipped) and left alone. Already settled files keep their result. Results the caller already decided — the open buffer's file, applied on the UI goroutine, or files with unsaved work — come in as `skip`; a decided write names the layers it wrote along (`FileResult.Settles`), and those are settled too.
- `WriteStaged` — the write itself: new files get their folder created inside the jail, and every write goes through `filetree.WriteViewFile` (atomic).

### layers.go

- `Latest` — a file's newest content: the top staged layer of any pending session, or the disk; with the session it came from. A disk file comes back whatever its size — the caller caps what it sends.
- `StagedIn` — the session on top of a file's layers, `""` for none (the `staged` socket command, for the Claude Code hook).
- `topLayer` — the walk described under Layers.
- `baseFor` — a newly staged file's base: the top layer, else the disk (`readBase`).
- `ApplyCheck` — whether a staged file can be written, and which pending sessions' copies are written along (`below`); else why not ("changed on disk", "edit session A changed it after this session built on it", "built on edit session A's edits, which were discarded").
- `PendingBase` — the pending session a file was built on, `""` when it was built on the disk or the base session is over; the UI asks it before applying.
- `SettleFile` — records a result on a pending session's file, saved like an edit.
- `Load` — picks up pending sessions from an earlier run at startup.

*Plus small helpers: `Snapshot`, `Pending`, `Finished`, `Log`, `WithClock`, `DiskMatchesBase`, `setFor`, `dropIfEmpty`, `readBase`, `readDisk`, `fileIn`, `pendingSet`, `applyFile`, `noSession`, `replaceText`, `matchLinesText`, `lineAt`, `lineCount`, `splitLines`, `countFiles`, `abs`.*

# internal/staging

**Introduction**

Agent edits that wait in the editor instead of landing in the files. A coding agent sends an edit session's edits over the socket (`ntee --msg --msg-edit-session <id> write|replace …`); this package keeps them on a staged copy of each file, so the files on disk stay untouched while the agent works. When the agent ends the session, the editor either applies them all at once or opens them for the user to review first; that choice is the user's setting (Ctrl+T › agent edits) and lives in `internal/app`. Agents that speak MCP reach the same commands as tools through `ntee --mcp` ([mcpserver.md](mcpserver.md)). Any agent can read a file's latest content through the editor — staged edits included — and edits to a file another session staged build on them (Layers, below). Applying never overwrites a file that changed on disk after it was staged.

Every session is one ntee-db record (`store.AgentEditSession`): each file's content before and after, and each edit as the agent sent it. The record is saved after every edit and stays after the session finishes, so which file contents an agent changed — and what became of them — can be traced later.

**Architecture**

`Registry` holds one workspace's edit sessions: the pending ones (`stagedSet`, by session id) and, for this run, the finished ones without their contents (so a late `wait` hears the outcome and a finished id is not reused). Edits arrive on the socket's connection goroutines; the UI reads snapshots.

Concurrency rule: a pending session's record is copy-on-write. An edit takes the set's `saveMu` (so edits and saves of one session run in order), builds the next record from a snapshot, saves it to the log, and only then publishes it under the short `mu`. A failed save leaves the session exactly as it was, and a reader never waits on a db write — it only takes `mu` to copy the record header, sharing slices that are never mutated in place.

A session starts on its first edit (a refused first edit leaves none behind) and takes edits until its agents' end (`MarkAgentEnded`).

**Agents.** Several agents may contribute to one session: each edit names its agent (`""` = unnamed), an agent's first edit adds it to the session's `Agents`, and every edit records who sent it. `MarkAgentEnded(session, agent)` ends that agent's part; only when no agent is still editing does the session get `AgentEndedAt` and become the user's to review — an agent that ended may take its part up again with another edit until then. `ForceEnd` is the user no longer waiting: the agents still editing are marked `Stopped`, their later edits are refused, the checks still to come are skipped, and the session is ready.

**Stages.** A session (`Checks`, set on new ones) goes **editing** → **finalising** → **ready** (`store.AgentEditSession.Phase`). It finalises when its last agent ends: no one may join any more (a newcomer's edit is refused and pointed at its own session), its sandbox is built once from the complete edits, and each agent reports its build and tests there (`RecordCheck`: passed, failed or skipped, with a note). Once every agent reported, `ReadyAt` is set and the session is the user's. An edit by one of its own agents while it finalises is a fix: the session goes back to editing, every check is cleared, its sandbox is dropped (`EditResult.Reopened`), and it finalises again once that agent ends. A ready session takes no edits — until the user asks its agents for changes (`RequestChanges`): it goes back to editing, with every agent the user did not stop editing again, the checks cleared and the sandbox dropped, and the request is logged in the record's `ChangeRequests` (stored as `[]` when there are none; a legacy record loads with it empty). The next edit or end from one of its agents answers the open request (`AnsweredAt`/`AnsweredBy`, in the save that edit makes anyway). Older records (no `Checks`) are ready as soon as their agents end. It then waits for `Finish` (apply results recorded) or `Discard`, both of which move it to the log's finished records, trimmed to the newest `keep`.

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
- `MarkAgentEnded`, `RecordCheck`, `ForceEnd`, `Finish`, `Discard` — the end of a session (see Agents and Stages above for the first three).
- `RequestChanges` — the user asking a ready session's agents for changes from the review: back to editing for every agent not stopped, checks cleared, sandbox dropped, the request logged. Refused unless the session is ready, and when every agent was stopped (there is no one to ask). `answerChangeRequest` stamps the open request when an agent edits or ends. `Finish` sets each file's `Result` from the apply results and derives the status (`applied`, `partly-applied`, `not-applied`) and an outcome sentence naming each file not written and why.
- `Registry.ApplyFiles` — writes a session's staged files to disk, run off the UI goroutine. A file is written only if `ApplyCheck` allows it; otherwise it is `conflicted` (or skipped) and left alone. Already settled files keep their result. Results the caller already decided — the open buffer's file, applied on the UI goroutine, or files with unsaved work — come in as `skip`; a decided write names the layers it wrote along (`FileResult.Settles`), and those are settled too.
- `WriteStaged` — the write itself: new files get their folder created inside the jail, and every write goes through `filetree.WriteViewFile` (atomic).

### sandbox.go

Builds, tests and searches see staged edits only once they are applied, so a session gets a **sandbox** to check them first: a scratch folder (`<ExternalDir>/sandboxes/<session>`, set with `WithSandboxDir`) holding a copy of every repo the session's staged files are in — every agent's, in a shared session — laid out like the workspace, so paths between repos still resolve, with the staged contents in place.

- `Sandbox` — builds or refreshes it, only once the session finalises (an agent still editing would leave half its change in the copy another agent tests). Each file's unit is its git repo inside the workspace (`repoOf`/`findRepo`: the nearest folder above it with `.git`, cached per file), else its top-level folder; `"."` when the workspace is the repo. Units not in the box yet are copied (`copyUnit`): an APFS copy-on-write clone where there is one (`cloneTree`, darwin; a whole repo in about the time a listing takes, ignored folders such as `node_modules` included), else a plain copy without `.git` that gives up past `maxSandboxCopyBytes` (`copyTree`). A copy lands under a `.building` name and is renamed in, so a failed one leaves nothing half-built. Every call then puts the session's unsettled staged files in place (`placeFile`: a file already holding its content is left alone, mtime included, and a changed one is written beside it and renamed in, so an agent building there while another refreshes never reads a half-written file); `include` copies in more folders a build needs, `fresh` rebuilds from the disk as it is now. A per-session lock serializes builds apart from the session's `saveMu`, so edits never wait on a copy; it runs on the socket goroutine.
- `DropSandbox` — deletes it: renamed to `.trash-…` at once and removed in the background (`trashSandbox`), after any build in progress. `finish` drops it when the session is applied or discarded; `Load` sweeps the trash and every sandbox no pending session owns (`sweepSandboxes`).
- `SessionsInRepo` — the sessions still open for edits that have staged files in a file's repo, with who still edits them, so an agent about to change that repo can join one (the receiver puts them in `read`'s detail and in a new session's first reply).

*Plus: `RepoName`, `unitName`, `unitContains`, `outermostUnits`, `copyFile`, `sandboxStateFor`, `sandboxPath`.*

### versions.go

Several agents may edit one file; their edits apply one at a time in arrival order. A replace protects itself — its old text must still match — but a whole-file write does not, and neither tells an agent that someone else changed those lines. So every content has a **version** (`Version`: a short SHA-256), reads and edit replies name it, and an edit may carry the version it was made on (`Replacement.Expect`, `WriteExpecting`).

- **Stale reads.** When the content an edit lands on is not the version the agent read, `staleReadOf` says who changed it since (the edits after that version in the file's log, else the session below or the disk) and where (`changedRanges`, a line diff from the old content kept in a bounded memory cache, `Remember`). A whole-file write is then refused, since it would undo that change; a replace on the changed lines is refused too (an unknown old version counts as overlapping), and one elsewhere goes through with a note (`EditResult.Notes`).
- **Same lines.** An edit landing on lines another agent changed earlier in the session gets a note naming them (`othersOnLines`, each earlier edit's lines moved by the edits after it).
- **Layers.** When a session's edit changes a copy other sessions built on (`rebaseDependents`), each of them still being edited has its edits replayed onto the new copy (`rebase`: its replaces in order; a whole-file write, or a replace whose old text is gone, cannot be replayed); one finalising or ready is left as it is. `EditResult.Rebased` reports each, so the receiver tells the agents at once instead of at apply time; `StaleNote` says why a copy that could not be replayed won't apply.

*Plus: `editSpan`, `lineRange`, `lineSpanText`, `versionCache.recall`.*

### layers.go

- `Latest` — a file's newest content: the top staged layer of any pending session, or the disk; with the session it came from. A disk file comes back whatever its size — the caller caps what it sends.
- `StagedIn` — the session on top of a file's layers, `""` for none (the `staged` socket command, for the Claude Code hook).
- `topLayer` — the walk described under Layers.
- `baseFor` — a newly staged file's base: the top layer, else the disk (`readBase`).
- `ApplyCheck` — whether a staged file can be written, and which pending sessions' copies are written along (`below`); else why not ("changed on disk", "edit session A changed it after this session built on it", "built on edit session A's edits, which were discarded").
- `PendingBase` — the pending session a file was built on, `""` when it was built on the disk or the base session is over; the UI asks it before applying.
- `SettleFile` — records a result on a pending session's file, saved like an edit.
- `Load` — picks up pending sessions from an earlier run at startup.

*Plus small helpers: `AgentLabel`, `AgentNames`, `agentList`, `stoppedAgents`, `Snapshot`, `Pending`, `Finished`, `Log`, `WithClock`, `DiskMatchesBase`, `setFor`, `dropIfEmpty`, `readBase`, `readDisk`, `fileIn`, `pendingSet`, `applyFile`, `noSession`, `replaceText`, `matchLinesText`, `lineAt`, `lineCount`, `splitLines`, `countFiles`, `abs`.*

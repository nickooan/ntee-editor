# internal/staging

**Introduction**

Agent edits that wait in the editor instead of landing in the files. A coding agent sends an edit session's edits over the socket (`ntee --msg --msg-edit-session <id> write|replace …`); this package keeps them on a staged copy of each file, so the files on disk stay untouched while the agent works. When the agent ends the session, the editor either applies them all at once or opens them for the user to review first; that choice is the user's setting (Ctrl+T › agent edits) and lives in `internal/app`. Applying never overwrites a file that changed on disk after it was staged.

Every session is one ntee-db record (`store.AgentEditSession`): each file's content before and after, and each edit as the agent sent it. The record is saved after every edit and stays after the session finishes, so which file contents an agent changed — and what became of them — can be traced later.

**Architecture**

`Registry` holds one workspace's edit sessions: the pending ones (`stagedSet`, by session id) and, for this run, the finished ones without their contents (so a late `wait` hears the outcome and a finished id is not reused). Edits arrive on the socket's connection goroutines; the UI reads snapshots.

Concurrency rule: a pending session's record is copy-on-write. An edit takes the set's `saveMu` (so edits and saves of one session run in order), builds the next record from a snapshot, saves it to the log, and only then publishes it under the short `mu`. A failed save leaves the session exactly as it was, and a reader never waits on a db write — it only takes `mu` to copy the record header, sharing slices that are never mutated in place.

A session starts on its first edit (a refused first edit leaves none behind) and takes edits until the agent's end (`MarkAgentEnded`). It then waits for `Finish` (apply results recorded) or `Discard`, both of which move it to the log's finished records, trimmed to the newest `keep`.

A file's base is read on its first edit, through the workspace jail (`filetree.ReadContainedFile`). A missing file is a new file. Binary files and files over `MaxFileBytes` (2 MiB, the review page's limit) are refused. Edits stack: a replace works on the content earlier edits staged.

**Functions**

### staging.go

- `Write` — stages a file's whole new content. The edit's line stats (`LineChanges` against the previous staged content) go into the record and the agent's reply.
- `Replace` — stages an exact-text replacement. The old text must match exactly once (`matchOffsets`, non-overlapping), or every match with `All`. With several matches and no `All`, a `HintLine` picks the match covering that line, else the nearest, the earlier on a tie (`pickMatch`); without one the agent is told how many matches there are and on which lines. The reply carries the changed lines in the new content.
- `edit` — the shared edit path: snapshot, apply, save, publish, as described under Architecture.
- `Content` — a file's staged content, or its disk content when the session has not staged it. This is what `show` prints, since the agent's own file reads see the disk.
- `MarkAgentEnded`, `Finish`, `Discard` — the end of a session. `Finish` sets each file's `Result` from the apply results and derives the status (`applied`, `partly-applied`, `not-applied`) and an outcome sentence naming each file not written and why.
- `ApplyFiles` / `ApplyFile` — write staged files to disk, run off the UI goroutine. A file is written only while its disk content still equals its staged base (`DiskMatchesBase`); otherwise it is `conflicted` and left alone. New files get their folder created inside the jail, and every write goes through `filetree.WriteViewFile` (atomic). Results the caller already decided — the open buffer's file, applied on the UI goroutine, or files with unsaved work — come in as `skip`.
- `Load` — picks up pending sessions from an earlier run at startup.

*Plus small helpers: `Snapshot`, `Pending`, `Finished`, `Log`, `WithClock`, `setFor`, `dropIfEmpty`, `readBase`, `noSession`, `replaceText`, `matchLinesText`, `lineAt`, `lineCount`, `splitLines`, `countFiles`, `abs`.*

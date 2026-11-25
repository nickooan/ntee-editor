# internal/extmsg

**Introduction**

The external-message channel: each running editor listens on a unix socket for its project, so other programs — coding agents, scripts, `ntee --msg` — can ask it to open a file at a line, to review a set of changed files, or to stage file edits. The package knows the wire format, the server, and the client; what a request *does* in the editor lives in `internal/app` (`ExternalReceiver`, `external.go`, review mode).

**Architecture**

A request is plain text: the first non-blank line is the command and its arguments, every following non-blank line one path (`open` takes exactly one, optionally with a `#L<n>` anchor; `review` takes 1–500). Either command may name one base revision after it on the first line (`open a1b2c3d`, `review v1.2.0`): a commit, tag, branch or `HEAD~2`, always compared with the current files on disk, uncommitted changes included. No revision means `HEAD`. An `open` may end with a note: after the path, a line that is exactly `---` ends the paths, and everything after it is free-form text the editor shows in a box next to the anchored line (blank lines and indentation kept, blank lines at either end dropped, at most `MaxLineNoteBytes`, 16 KiB). A note needs a `#L<n>` anchor — the app's receiver refuses one without, since anchors are parsed there — and `review` refuses a note. A request may also name a guided session with the first-line option `--session=<id>` (`open --session=tour1`): each step of an agent's walkthrough is an `open` with a note, queued by the editor, and two more commands act on the session — `end` (no more steps will come) and `wait` (block until the user finishes or closes the session); both take only the option, except that `wait` may also carry `--wait=<duration>` (`WaitOption`, any other command refuses it): how long the sender will wait, so the editor answers `ok waiting …` just before then instead of holding the connection after the sender has gone. `--diff-session=<id>` names a diff session instead, whose every step the editor shows in the diff view against the step's base revision (`HEAD` when none is named); a `--session=` step takes no base revision, since a note session never shows a diff. `--review-session=<id>` names a review session: each `review` carries exactly one file — its `#L<n>` anchor optional — and an optional note after `---`, and the editor adds it to a live review. One option per request; a note or diff session's `open` needs a note; `open` takes no review session and `review` no other kind; a plain `review` takes no note; any other `--…` token is refused as an unknown option. A step is answered at once (`ok step N queued`); a `wait` is answered by the *user* finishing (`ok finished …`) or ending the session, so it may wait up to `MaxSessionWait`, or its `--wait=` budget when shorter. A third reply kind, `ended <reason>`, tells the agent the session is over and to stop. `--edit-session=<id>` names an edit session (`SessionEdit`), whose commands stage edits instead of writing files: `write` and `replace` name one path and carry a **payload** after the `---` line — a write's new content, a replace's `{"old","new","all"}` JSON (`ReplacePayload`, `EncodeReplace`/`DecodeReplace`) — kept byte for byte (no CRLF normalising, no trimming), so it can be any file content, `---` lines included; `show` names one path; `status` and `discard` take only the option; `end` and `wait` act on it like on any session. None of them takes a base revision or a note, and `open`/`review` refuse the option. `write`, `replace`, `end` and `status` may name the sending agent with `--agent=<id>` (`AgentOption`, `Message.Agent`), so several agents can share one session; any other command refuses it. `write` and `replace` may name the file version the edit was made on with `--expect=<version>` (`ExpectOption`, `Message.Expect`; `WithExpect` adds it to a request), as a read's detail (`· version <v>`) or the previous edit's reply named it. `check` (`--edit-session=`, optional `--agent=`) is an agent's report on its finalising session: one result argument, `pass`, `fail` or `skip` (`CheckPass`…), and an optional note after `---` saying what it ran (`CheckRequest` builds it). `sandbox` (`--edit-session=` only) asks for the session's sandbox: an optional `fresh` or `drop` argument (`SandboxFresh`, `SandboxDrop`), then the folders to copy in too, one per line (`SandboxRequest` builds it). Two reads are for any agent, with the edit session optional: `read` names one path and answers its latest content — the session's own staged copy, else the newest edits any session staged, else the disk — and `staged` names one path and answers `ok staged · edit session <id>` or `ok not staged` (what the Claude Code hook asks before a built-in file tool runs). The sender writes it and half-closes the connection; the server reads to EOF and answers one line, `ok <detail>` or `error <reason>` — except that `show`, `read` and `status` answer with a body (`BodyReply`: `ok <bytes>`, a newline, then the content byte for byte), which `SendForBody` reads. A `read`'s first line also says where the content came from (`BodyReplyWith`: `ok <bytes> staged · edit session <id> · not written`, or `ok <bytes> disk`).

The socket is `~/.ntee-editor/external/<project hash>/ntee.sock` (`store.ExternalSocketPath`, the same hash as the project's store). Trust is the file system's: the directory is 0700 and the socket 0600, so only the user's own processes can connect, and the app jails every named path to the opened folder. Messages are off by default (a global switch in Ctrl+T › external); the socket listens either way so a sender gets "disabled" instead of a dead socket.

Each connection is served on its own goroutine with a 2 s read deadline and a 1 MiB cap, so a slow or hostile sender can't hold the editor; the handler never runs on the UI goroutine. At most `maxConnections` (128) are served at once — a `wait` holds its connection until the user is done — and a connection past that is still read, then answered `error the editor is busy …`, so senders can't pile up goroutines and file descriptors. Only one editor per project listens: a live socket makes a second instance report `ErrInUse`, while a leftover file from a crashed instance (nobody answers) is replaced.

### Which editor gets a message

There is no registry of running editors. The socket path is derived from the opened folder alone, so the editor and the sender compute it independently:

1. Take the folder's absolute path (`filepath.Abs`, e.g. `/Users/me/workspaces`).
2. Hash it with SHA-256 and keep the first 16 hex characters.
3. The socket is `~/.ntee-editor/external/<those 16 chars>/ntee.sock`. The project's message history sits next to it in `…/<hash>/history`, and the project's store uses the same hash under `~/.ntee-editor/stores/` (`projectDir` in store.go).

At startup an editor computes this for the folder it opened and listens there. `ntee --msg` (`findEditorSocket` in cmd/ntee/msg.go) picks a target like this:

- **`--msg-root <dir>`** — that folder's socket, no search.
- **Otherwise, `Discover` from the current directory:**
  - It checks the current directory, then each parent up to `/`.
  - For each folder it computes the socket path and probes it: it connects and closes without sending anything, which `serve` ignores.
  - The first folder whose socket answers wins, so the **nearest enclosing** editor gets the message.
  - A missing file means no editor has that exact folder open. A file nobody answers on is left over from a crash. Either way the search moves to the parent.
- **If nothing encloses the current directory**, it runs `Discover` again from the folder of the first path in the message. This covers a sender running outside the project.
- **If neither search finds an editor**, the sender fails with `ErrNoEditor`.

Consequences worth knowing:

- **Upward only.** An editor on a subfolder or a sibling of the current directory is never chosen. The one exception is the second search, when the first path lies inside it.
  - Example: editors are open on `~/ws` and `~/ws/app`. A sender in `~/ws/app/src` reaches `~/ws/app`; a sender in `~/ws/lib` reaches `~/ws`.
- **The same folder open twice.**
  - The first editor holds the socket. The second gets `ErrInUse`, does not listen, and its Ctrl+T › external panel shows "not listening" with the reason.
  - All messages reach the first editor.
  - When the first editor exits, the second does not take over. The folder has no listener until an editor is started on it again.
- **Single-file mode** (`ntee <file>`) never listens, so it is never a target.
- **Symlinks are not resolved.** Both sides hash the path as given, made absolute. An editor opened through a symlinked path and a sender working under the real path (or the other way round) compute different sockets, and the sender won't find the editor. `--msg-root` with the path the editor was opened with works around it.

**Functions**

### extmsg.go

- `Parse` — reads a request into `Message{Command, Args, Paths, LineNote, Session}`, normalizing CRLF, dropping blank lines, and lower-casing the command. At the first `LineNoteSeparator` line after the command it stops reading paths and keeps the rest verbatim as the note (`trimBlankLines` drops blank lines at either end and trailing spaces). It enforces each command's shape (open: one path and a note of at most `MaxLineNoteBytes`; review: 1–`MaxReviewPaths` paths and no note; both: at most one base revision, never one starting with `-`, which would reach git's argv as an option — `checkBaseArgument`). Its errors go back to the sender verbatim, so they say what to fix.

First-line tokens starting with `--` are options: `--session=<id>`, `--diff-session=<id>` or `--review-session=<id>` (`SessionOption`/`DiffSessionOption`/`ReviewSessionOption`, the id checked against `[A-Za-z0-9._-]{1,64}`) becomes `Message.Session`, and `Message.SessionKind` says which (`SessionNote`, `SessionDiff`, `SessionReview`; `SessionKind.Option` maps back, `sessionKindOf` reads a token); two at once, or anything else, is refused; the rest are the arguments (the base revision), which a `--session=` open refuses. `end` and `wait` need a session option (any kind — the id names the session) and take nothing else, but a `wait`'s `--wait=<duration>` budget (`Message.WaitBudget`, a positive Go duration).

For `write` and `replace` (`carriesPayload`), the text after the first separator line is `Message.Payload` instead of a note, read from the raw request so it stays byte for byte; `HasPayload` tells an empty payload (an empty file) from none. `checkEditSessionCommand` holds what every edit session command shares (the option, no base, no note), and a replace's payload is decoded once here so a malformed one is refused before it reaches the editor.

*Plus: `Message.Base` (the base revision, "" for HEAD), `OkReply`, `ErrorReply`, `IsErrorReply`, `EndedReply`, `IsEndedReply`, `ChangesRequestedReply`, `IsChangesRequestedReply` (an edit session's `wait` when the user asks its agents for changes: `ok changes requested — …`, exit 5 for the CLI), `oneLine` — the reply format; `BodyReply`, `BodyReplyWith`, `SplitBodyReply` (the size is the first word after `ok`), `BodyDetail` — a reply carrying content.*

### server.go

- `Listen` — prepares the private directory, probes an existing socket file (live → `ErrInUse`; dead → removed), listens, and restricts the socket to the user. `acceptLoop` serves each connection on its own goroutine, holding one of the server's `connectionSlots` while it does (none free → the busy reply), and backs off briefly on transient accept errors.
- `serve` — reads one request up to the half-close (deadline + size cap), skips empty connections (liveness probes from `Discover` or a second instance), and writes the handler's reply line. An oversize request is drained (still under the deadline) before the refusal is written, so the sender's in-flight write doesn't fail before it reads the answer.
- `Close` — stops accepting and removes the socket file; requests already being served finish on their own.

*Plus: `Path`.*

### client.go

- `Send` — dials, writes the request, half-closes (the end-of-request marker), and reads the reply line, all under a deadline. `SendWithTimeout` takes that deadline from the caller: a session's `wait` waits for the user (`--msg-wait`), far longer than a plain request's 10 s. The CLI's `deliverWait` (cmd/ntee/msg.go) sends that `--msg-wait` as the wait's `--wait=` budget, so the editor lets go of the connection before the client does; an editor older than the option refuses it as unknown, and the bare `wait` goes out once instead. It reads the reply even when the write failed: an editor that answers early and hangs up (an oversize refusal) still gets its answer through.
- `Discover` — walks up from a directory to the nearest ancestor that is the opened folder of a running editor (its socket answers), given the folder→socket mapping. `ntee --msg` uses it so a sender only needs to run somewhere inside the project. See [Which editor gets a message](#which-editor-gets-a-message).

- `SendForBody` — `SendWithTimeout` for a request whose reply may carry a body (`show`, `read`, `status`): it reads up to `MaxBodyBytes` and splits the reply with `SplitBodyReply`, keeping the body byte for byte.
- `exchange` — the one dial–write–read all senders share. It takes a context whose cancellation closes the connection, so an MCP client cancelling a pending `edit_wait` frees it at once. A connection the editor closes without a reply line is `ErrNoReply`, never an empty "success": the editor always answers, so silence means it quit or restarted while holding the request (typically a `wait`). The CLI reports it with exit 1, plus a hint for a session to wait again.

- `BuildRequest` — a request's wire text: the command, its session option and base revision on the first line, one path per line, then the note after `---`. The `ntee --msg` client and the MCP show tools (`show_code`, the walkthrough) both build their requests with it, so the two can never drift apart.

*Plus: `writeRequest` (write + half-close), `isLive` — the liveness probe, `trimReply`.*

### edit_replies.go

Edit session replies are written for people, but the MCP tools and the Claude Code hook act on a few of their parts. This file is the one home of those parts, so the editor writing a reply and a client reading it cannot drift apart.

- The phrases a client branches on: `PhraseUserReviewing`, `PhraseStartNewSession` (the session takes no more edits; staging's `ErrClosedForEdits` carries it, pinned by a test in app), `PhraseFinalising`, `PhraseBackToEditing` and `PhraseSetAside` (what a `wait` answered "waiting" for).
- Builders and their parsers: `WithVersion`/`VersionIn` (`· version <v>`), `ReadSourceDetail`/`StagedSessionIn` (where a read came from), `WithStale`/`StaleIn`, `WithAlsoEditing`/`AlsoEditingIn`, and `StagedReply`/`StagedReplySession` for the `staged` request.
- The send timeouts shared by `ntee --msg` and the MCP tools: `EditRequestTimeout` (10 s), `EditEndTimeout` (45 s — the editor may apply the files first), `EditSandboxTimeout` (60 s), and `EditorEndWait` (40 s), how long the editor holds an end, kept under the sender's (a test pins it).

`SessionKind.MsgFlag` (extmsg.go) is the `ntee --msg` flag of a session kind, which both the CLI and the editor's replies name.

### edit_client.go

The sending side of edit sessions and reads, shared by `ntee --msg --msg-edit-session` / `ntee --msg read|staged` (cmd/ntee/msg_edit.go), the Claude Code hook (cmd/ntee/hook.go) and the MCP tools (`internal/mcpserver`), so both put the same bytes on the wire.

- `EditRequest` — an edit command's wire text: command and `--edit-session=` option (left out for a `read` or `staged` without a session), the `--agent=` option for `write`/`replace`/`end`/`status` when an agent is given, the file when there is one, and for `write`/`replace` the payload after the `---` line. A request over `MaxMessageBytes` is refused before it is sent.
- `EditWaitRequest` — an edit session's `wait` carrying the sender's `--wait=` budget.
- `SendEdit` — sends one edit request under a context: `show`, `read` and `status` read a body reply, the rest one line.

# internal/extmsg

**Introduction**

The external-message channel: each running editor listens on a unix socket for its project, so other programs — coding agents, scripts, `ntee --msg` — can ask it to open a file at a line or to review a set of changed files. The package knows the wire format, the server, and the client; what a request *does* in the editor lives in `internal/app` (`ExternalReceiver`, `external.go`, review mode).

**Architecture**

A request is plain text: the first non-blank line is the command and its arguments, every following non-blank line one path (`open` takes exactly one, optionally with a `#L<n>` anchor; `review` takes 1–500). Either command may name one base revision after it on the first line (`open a1b2c3d`, `review v1.2.0`): a commit, tag, branch or `HEAD~2`, always compared with the current files on disk, uncommitted changes included. No revision means `HEAD`. The sender writes it and half-closes the connection; the server reads to EOF and answers one line, `ok <detail>` or `error <reason>`.

The socket is `~/.ntee-editor/external/<project hash>/ntee.sock` (`store.ExternalSocketPath`, the same hash as the project's store). Trust is the file system's: the directory is 0700 and the socket 0600, so only the user's own processes can connect, and the app jails every named path to the opened folder. Messages are off by default (a global switch in Ctrl+T › external); the socket listens either way so a sender gets "disabled" instead of a dead socket.

Each connection is served on its own goroutine with a 2 s read deadline and a 1 MiB cap, so a slow or hostile sender can't hold the editor; the handler never runs on the UI goroutine. Only one editor per project listens: a live socket makes a second instance report `ErrInUse`, while a leftover file from a crashed instance (nobody answers) is replaced.

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

- `Parse` — reads a request into `Message{Command, Args, Paths}`, normalizing CRLF, dropping blank lines, and lower-casing the command. It enforces each command's shape (open: one path; review: 1–`MaxReviewPaths` paths; both: at most one base revision, never one starting with `-`, which would reach git's argv as an option — `checkBaseArgument`). Its errors go back to the sender verbatim, so they say what to fix.

*Plus: `Message.Base` (the base revision, "" for HEAD), `OkReply`, `ErrorReply`, `IsErrorReply`, `oneLine` — the reply format.*

### server.go

- `Listen` — prepares the private directory, probes an existing socket file (live → `ErrInUse`; dead → removed), listens, and restricts the socket to the user. `acceptLoop` serves each connection on its own goroutine and backs off briefly on transient accept errors.
- `serve` — reads one request up to the half-close (deadline + size cap), skips empty connections (liveness probes from `Discover` or a second instance), and writes the handler's reply line. An oversize request is drained (still under the deadline) before the refusal is written, so the sender's in-flight write doesn't fail before it reads the answer.
- `Close` — stops accepting and removes the socket file; requests already being served finish on their own.

*Plus: `Path`.*

### client.go

- `Send` — dials, writes the request, half-closes (the end-of-request marker), and reads the reply line, all under a deadline. It reads the reply even when the write failed: an editor that answers early and hangs up (an oversize refusal) still gets its answer through.
- `Discover` — walks up from a directory to the nearest ancestor that is the opened folder of a running editor (its socket answers), given the folder→socket mapping. `ntee --msg` uses it so a sender only needs to run somewhere inside the project. See [Which editor gets a message](#which-editor-gets-a-message).

*Plus: `writeRequest` (write + half-close), `isLive` — the liveness probe.*

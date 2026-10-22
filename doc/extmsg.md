# internal/extmsg

**Introduction**

The external-message channel: each running editor listens on a unix socket for its project, so other programs — coding agents, scripts, `ntee --msg` — can ask it to open a file at a line or to review a set of changed files. The package knows the wire format, the server, and the client; what a request *does* in the editor lives in `internal/app` (`ExternalReceiver`, `external.go`, review mode).

**Architecture**

A request is plain text: the first non-blank line is the command and its arguments, every following non-blank line one path (`open` takes exactly one, optionally with a `#L<n>` anchor; `review` takes 1–500 and an optional base revision). The sender writes it and half-closes the connection; the server reads to EOF and answers one line, `ok <detail>` or `error <reason>`.

The socket is `~/.ntee-editor/external/<project hash>/ntee.sock` (`store.ExternalSocketPath`, the same hash as the project's store). Trust is the file system's: the directory is 0700 and the socket 0600, so only the user's own processes can connect, and the app jails every named path to the opened folder. Messages are off by default (a global switch in Ctrl+T › external); the socket listens either way so a sender gets "disabled" instead of a dead socket.

Each connection is served on its own goroutine with a 2 s read deadline and a 1 MiB cap, so a slow or hostile sender can't hold the editor; the handler never runs on the UI goroutine. Only one editor per project listens: a live socket makes a second instance report `ErrInUse`, while a leftover file from a crashed instance (nobody answers) is replaced.

**Functions**

### extmsg.go

- `Parse` — reads a request into `Message{Command, Args, Paths}`, normalizing CRLF, dropping blank lines, and lower-casing the command. It enforces each command's shape (open: one path, no arguments; review: 1–`MaxReviewPaths` paths, at most one base revision, never one starting with `-`, which would reach git's argv as an option). Its errors go back to the sender verbatim, so they say what to fix.

*Plus: `Message.Base`, `OkReply`, `ErrorReply`, `IsErrorReply`, `oneLine` — the reply format.*

### server.go

- `Listen` — prepares the private directory, probes an existing socket file (live → `ErrInUse`; dead → removed), listens, and restricts the socket to the user. `acceptLoop` serves each connection on its own goroutine and backs off briefly on transient accept errors.
- `serve` — reads one request up to the half-close (deadline + size cap), skips empty connections (liveness probes from `Discover` or a second instance), and writes the handler's reply line.
- `Close` — stops accepting and removes the socket file; requests already being served finish on their own.

*Plus: `Path`.*

### client.go

- `Send` — dials, writes the request, half-closes (the end-of-request marker), and reads the reply line, all under a deadline.
- `Discover` — walks up from a directory to the nearest ancestor that is the opened folder of a running editor (its socket answers), given the folder→socket mapping. `ntee --msg` uses it so a sender only needs to run somewhere inside the project.

*Plus: `isLive` — the liveness probe.*

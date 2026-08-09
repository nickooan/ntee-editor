# internal/lsp

**Introduction**

This package is the editor's language-server client. It speaks JSON-RPC 2.0 over stdio to servers like gopls and typescript-language-server, and hands the rest of the app a small, safe surface: a `Registry` that resolves "which server handles this file?" and a `Client` with doc-sync calls plus definition, references, and completion queries.

The design is shaped by a few hard constraints:

- **One server per language, started lazily.** The first file you open in a language spawns its server; later projects are added as workspace folders on the same server rather than spawning more processes. Each server is scoped to the file's nearest project root (tsconfig, go.mod, …), so a monorepo frontend indexes its own few hundred files, not the whole tree.
- **A wedged server can never freeze the UI.** All doc-sync notifications (didOpen, didChange, …) go through a bounded per-client op queue drained by a single writer goroutine. The blocking pipe write happens on that goroutine with no locks held. If the server stops consuming stdin, the writer parks — not the UI goroutine.
- **Notices are collected, then emitted.** The message sink is Bubble Tea's `program.Send`, which is unbuffered — and the UI goroutine it hands to may itself be blocked waiting on the Manager's lock. So nothing emits while holding `Manager.mu`; notices accumulate in a slice and are drained after unlock.
- **Crashes heal, crash loops don't.** A dead server is replaced on the next demand, but rapid successive deaths burn a per-language restart budget and disable the language for the session. A server that ran a long time before dying resets the budget — its crash is news, not a loop.
- **Vue is a hybrid.** In Volar's hybrid mode the Vue server only answers template features; `<script>` intelligence comes from tsserver. The package carries bridge/mirror machinery to relay `tsserver/request` commands to the TypeScript server and mirror `.vue` document sync to it, and the Manager detects bridge cycles in config (self-bridge, A↔B) that would otherwise recurse forever.

When LSP is off or unconfigured, a no-op registry keeps every call site working with zero servers.

**Architecture**

Three layers, bottom up:

- **`Conn` (rpc.go)** — a hand-rolled, dependency-free JSON-RPC 2.0 endpoint over LSP `Content-Length` framing, ported from ntee-r1quest's jsonrpc package. Full duplex: outbound `Request`/`Notify`, inbound traffic dispatched to a `Handler`.
- **`serverClient` (client.go)** — one running server process: spawn, initialize handshake, the writer queue, crash reporting from a stderr tail, and the query methods.
- **`Manager` (registry.go)** — the real `Registry`: maps file extensions to languages, lazily starts one `serverClient` per language, manages restarts/disables, and wires the Vue↔TypeScript bridge.

Goroutines per running server:

- **`Conn.readLoop`** — reads frames off the server's stdout. Responses resolve pending requests; requests take a slot from a semaphore (cap 16) and run in a goroutine, so a flooding server backpressures the pipe instead of spawning without bound; notifications go to the ordered channel.
- **`Conn.notificationLoop`** — a single worker that dispatches notifications in arrival order (publishDiagnostics must not be reordered).
- **`serverClient.writerLoop`** — the only place doc-sync ops touch the child's stdin. Woken by a cap-1 `wake` channel; exits when the client dies (every death path pokes the channel).
- **`serverClient.watchExit`** — the sole owner of `cmd.Wait()`: marks the client dead, closes the connection to fail in-flight requests, and surfaces a crash notice unless the exit was a deliberate stop.
- **`serverClient.start`** itself runs on a goroutine spawned by the Manager, so a slow initialize never blocks the caller.

Lock order is strictly `Manager.mu` → `serverClient.mu` → `Conn` mutexes; nothing takes them in the other direction, and blocking I/O happens with no locks held. Queue behavior: an `opChange` for a path already queued replaces it in place (full-content sync means the newest supersedes losslessly). At the 1024-op cap, change/save ops are dropped with a one-shot notice, while open/close/folder — protocol-state critical and bounded by user actions — always append.

Data flow: the UI calls `Registry.ClientFor(path)` → Manager resolves language and project root, lazily starting the server → doc-sync calls enqueue ops → writerLoop writes them to stdin. Inbound diagnostics flow readLoop → notificationLoop → `handle` → the sink as `DiagnosticsMsg`. Queries (`Definition` etc.) are direct blocking `conn.Request` calls with a 12-second timeout — generous because a cold Vue→tsserver round-trip loading a TS project genuinely takes seconds (same "generous against the worst legitimate case" philosophy as `gitcmd.Timeout`).

File map:

| File | Role |
|---|---|
| `lsp.go` | Public surface: `Client`/`Registry` interfaces, message types, no-op registry |
| `rpc.go` | JSON-RPC 2.0 endpoint and LSP framing |
| `protocol.go` | Typed LSP structs, URI and UTF-16 conversions, response parsers |
| `client.go` | `serverClient`: process lifecycle, writer queue, queries, hybrid relay |
| `registry.go` | `Manager`: lazy start, restarts, enable/disable, bridge wiring |

**Functions**

### lsp.go

The interfaces and message types the rest of the app depends on. `Client` is one server session: doc-sync is fire-and-forget (queued until initialize finishes), while `Definition`/`References`/`Completion` return an error when the server isn't ready so callers report it instead of guessing. `Registry` adds `UnavailableReason` (the real cause — binary missing vs crash-looped — instead of a generic hint), `Statuses` for the inspection pane, and runtime `Enable`/`Disable`.

- `NewNoopRegistry` — the registry used when LSP is globally off: never resolves a client, and `Enable` explains that a restart is needed.

*Plus small helpers: the `noopRegistry` methods (`ClientFor`, `UnavailableReason`, `Statuses`, `Enable`, `Disable`, `ShutdownAll`) — trivial stubs returning "no".*

### rpc.go

- `NewConn` — builds a `Conn` over any `io.ReadWriteCloser` and starts `readLoop` and `notificationLoop`. The handler may be nil for an outbound-only peer.
- `Request` — sends a request and blocks until the response or context deadline. IDs are matched by raw bytes so numeric and string ids both round-trip.
- `Notify` — fire-and-forget notification.
- `Close` — shuts the connection and fails every in-flight request rather than leaving callers hanging.
- `readLoop` / `dispatch` — read frames and route them: responses to `resolveResponse`, requests to a semaphore-bounded goroutine, notifications to the ordered worker. Both request and notification paths bail out via `done` when the connection is closing.
- `notificationLoop` — the single worker that keeps notifications in arrival order.
- `resolveResponse` — matches a response to its pending request channel; tolerates spec-legal `"id": null`.
- `handleRequest` — runs the handler for a server→client request and writes back the result or a JSON-RPC error.
- `shutdown` — the one-shot teardown: marks closed and delivers the failure reason to every pending request.
- `readMessage` / `writeMessage` — the LSP `Content-Length` framing. `readMessage` rejects frames over a 32 MB cap (`maxFrameBytes`) so a broken or hostile server can't force an arbitrarily large allocation.

*Plus small helpers: `RPCError.Error`, `Message.isResponse`, `Conn.write` (serialized by `writeMu`), `marshalParams`.*

### protocol.go

Mostly typed structs for the LSP subset the editor speaks, plus the two offset bridges the protocol forces on us: paths ↔ `file://` URIs, and rune columns ↔ UTF-16 code units (LSP positions are UTF-16).

- `PathToURI` / `URIToPath` — the URI bridge; `URIToPath` refuses non-file schemes.
- `UTF16Col` / `RuneCol` — the column bridge in each direction, counting surrogate pairs as two units.
- `parseLocations` — accepts the three shapes a definition response can legally take: one `Location`, `[]Location`, or `[]LocationLink`.
- `parseCompletion` — accepts a `CompletionList`, a bare item array, or null.

The `clientCapabilities` var declares full-content sync, plain `Location` responses (no linkSupport), workspace folders, and `labelDetailsSupport` so servers attach signature suffixes and origin packages to completion candidates.

### client.go

- `newServerClient` — constructs the client with its wake channel, bridge semaphore, and the initial workspace folder; `startedAt` is stamped here so the registry can later tell a long-lived crash from a startup loop.
- `start` — spawns the process and runs the initialize handshake (20s timeout). It prepends the binary's own directory to PATH (script servers like typescript-language-server need their node runtime), retains a stderr tail for crash reports, and handles the race where a `stop()` landed mid-spawn by reaping the child itself. Any failure marks the client dead and surfaces exactly one notice.
- `resolveBinary` / `ResolveBinary` — finds the server executable: absolute path, then PATH, then `~/go/bin` (where `go install` puts gopls, often missing from GUI-shell PATHs). A relative path containing a separator is rejected outright — it would resolve against the launch directory, which the opened project controls. This pairs with the config trust rule (internal/config): project-local configs may not name executables at all; only the user config can. The exported alias exists so `--prepare-lsp` verifies installs with identical rules.
- `watchExit` — the sole `cmd.Wait()` owner: on exit it marks the client dead, closes the connection so in-flight requests fail instead of hanging, and — unless the exit was deliberate — emits a crash notice with the stderr-derived reason.
- `enqueue` — appends a doc-sync op, coalescing `opChange` per path in place and enforcing the 1024-op cap (change/save dropped with a one-shot notice; open/close/folder always append).
- `writerLoop` — drains the queue whenever poked and ready; the blocking stdin write happens with no locks held.
- `becomeReady` — installs the live connection and starts the writer (once). Returns false if a stop landed during the handshake, so the caller tears down instead of going live on a client the registry already dropped.
- `DidOpen` / `DidChange` / `DidSave` / `DidClose` — enqueue the corresponding notification and, for hybrid servers, mirror it to the companion. `DidChange` has a subtle upgrade: if the doc was never opened on *this* client (the server was restarted after a crash), it sends didOpen instead — servers drop a didChange for an unknown doc, and full-content sync makes the two equivalent.
- `EnsureFolder` — registers a new repo as a workspace folder (deduped at enqueue time), so opening a file in a second repo scopes the existing server instead of spawning another.
- `Definition` / `References` — blocking queries with the 12s timeout. For hybrid files they also ask the companion (tsserver has the `.vue` file via the plugin) and merge, since the Vue server only answers the template half.
- `Completion` — same query pattern, no companion merge.
- `ExecuteCommand` — `workspace/executeCommand`; the bridge's transport to the TypeScript server.
- `handleTsserverRequest` — the hybrid relay: unpacks Volar's `[id, command, args]` (tolerating the wire's extra array wrapping), relays via the bridge on a goroutine, and *always* replies `tsserver/response` — null on failure or when all 8 relay slots are busy — so the Vue server's awaited promise never hangs. The reply is double-wrapped `[[id, result]]` because vscode-jsonrpc spreads array params and Volar's handler takes one argument.
- `handle` — the server→client handler: diagnostics to the sink, `workspace/configuration` answered with nulls, `tsserver/request` to the relay, everything else tolerated silently.
- `stop` — deliberate shutdown: flags `stopping` so watchExit stays quiet, runs shutdown/exit with a short grace period, then kills and waits on the watcher rather than reaping the process itself.
- `describeExit` / `crashReason` — turn an exit status plus the stderr tail into a readable one-liner ("exit status 1 — TypeError: …"), preferring an error/panic-looking line and clipping for the status bar.
- `mergeLocations` — concatenates two location lists, dropping duplicates by URI + start position.

*Plus small helpers: `tailBuffer.Write`/`String` (a last-N-bytes stderr ring), `stdioConn.Read`/`Write`/`Close`, `envWithPathPrefix`, `languageIDFor`, `isDead`, `uptime`, `poke`.*

### registry.go

- `NewManager` — builds the extension→language map (immutable afterwards, read lock-free), seeds disabled reasons for config-disabled languages, and runs the bridge-cycle check so a config with a self-bridge or A↔B chain disables the language up front instead of recursing at runtime.
- `bridgeCycleReason` — follows a language's `bridge.To` chain and reports a disable reason when it revisits a language; "" when it terminates.
- `SetSink` / `emit` — connect the Manager to `program.Send` and flush anything emitted during startup; before the sink exists, up to 64 messages are buffered.
- `ClientFor` — the main entry: extension → language, file → project root (memoized), then `getOrStart`.
- `projectRootFor` — memoizes `filetree.FindProjectRoot` per directory in a lock-free `sync.Map`; the walk stats the disk, so it runs before `m.mu` is taken.
- `getOrStart` — `getOrStartLocked` plus the lock and the deferred notice drain: every notice produced under `m.mu` is emitted only after release (the collect-then-emit rule).
- `getOrStartLocked` — the core: returns the running server, or replaces a dead one on demand. A long-lived corpse resets the restart budget; the third rapid death disables the language with a visible reason. Fresh starts wire the bridge, mirror, and companion lookup for hybrid servers (with a runtime belt against self-bridging), and eagerly warm the companion so the first bridged request doesn't hit a cold server and time out.
- `UnavailableReason` — the honest error string: unmapped extension gets the `--prepare-lsp` hint, a disabled language gets its actual disable reason.
- `makeBridge` — builds the relay closure: forward a hybrid server's tsserver command to the companion via `workspace/executeCommand` and unwrap the tsserver envelope body.
- `makeMirror` — forwards a hybrid server's doc-sync to the companion so tsserver (with @vue/typescript-plugin) has the `.vue` file in a real project; without this the bridge relays but tsserver errors "file not in project" and Volar degrades to a limited inferred-project service.
- `Statuses` — every configured language's state (running/stopped/disabled + reason) for the inspection pane, sorted by name.
- `Enable` — clears disabled state and the restart budget, sets a runtime override past `enable: false` in config, and eagerly starts the server so the pane turns green now; sync-detectable failures come back in `reason`, async crashes arrive later as notices.
- `Disable` — stops the server (outside `m.mu`, mirroring ShutdownAll) and marks the language disabled for the session.
- `ShutdownAll` — collects every client under the lock, then stops them all outside it.

*Plus small helpers: `clientForLang` (explicit-language lookup used by the bridge), `fileFromArgs` (extract the tsserver command's target file), `unwrapBody` (peel the tsserver `{..., body}` envelope).*

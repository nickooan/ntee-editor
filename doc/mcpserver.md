# internal/mcpserver

**Introduction**

`ntee --mcp` serves the agent edit sessions ([staging.md](staging.md)) as MCP tools, so a coding agent stages its file edits in the user's running editor through typed tool calls: no shell heredocs, no `<<< :new` separator, no session ids or exit codes to manage. The server's `instructions` and tool descriptions teach the agent to use these tools instead of its own file-writing tools whenever an ntee editor is open on the project, and to run builds and tests only once the edits are applied.

Set it up once per agent, for example in Claude Code:

```
claude mcp add ntee -- ntee --mcp
```

The agent then sees `read_file`, `edit_replace`, `edit_write`, `edit_status`, `edit_discard`, `edit_end` and `edit_wait`. `read_file` reads a file's latest content through the editor — edits staged by any agent and not yet on disk included — so a second agent working on the project sees the first one's staged changes, and its edits to those files build on them.

The agent's own Read, Edit and Write tools only see the disk. To keep Claude Code from using them on a file with staged edits, add the optional hook `ntee --hook` to the project's `.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      { "matcher": "Read|Edit|MultiEdit|Write", "hooks": [{ "type": "command", "command": "ntee --hook" }] }
    ]
  }
}
```

Before each matched call the hook asks the running editor whether the file has staged edits (`staged`). If it has, the call is denied with a reason pointing at `mcp__ntee__read_file` / `edit_replace` / `edit_write`; otherwise — and whenever there is no editor to ask — it is allowed silently. The user may also deny the built-in `Edit` and `Write` tools outright in the project's permissions. Both are the user's choice; ntee configures nothing.

**Architecture**

The transport is stdio, not a socket or HTTP endpoint of the editor's own. MCP clients cannot reach a unix socket, and an HTTP listener would need a port per editor (which the client config would have to follow), a token, and Origin checks. The client instead starts one `ntee --mcp` process per agent session in the project folder. Each tool call becomes one request on the editor's existing socket, found with `findEditorSocket` like `ntee --msg` (`--msg-root` picks one). So the server follows the right project, outlives editor restarts, and says plainly when no editor is open: nothing was staged, and the agent should edit the usual way. The editor itself is unchanged; it cannot tell an MCP edit from a `--msg` one, since both send `extmsg.EditRequest` bytes.

The protocol is newline-delimited JSON-RPC 2.0, hand-rolled for its five methods (`initialize`, `ping`, `tools/list`, `tools/call`, and the `notifications/cancelled` notification) rather than pulling in an SDK. Stdout carries only protocol messages.

Concurrency rule: the reading loop dispatches in order, and each `tools/call` runs on its own goroutine with its own context, registered under its request id before the loop reads on. Responses are written whole under one mutex. So a long `edit_wait` never holds up an `edit_status`, and a cancellation always finds its call. A cancelled call closes its socket connection and sends no response, as MCP asks. When stdin ends, calls still running get to answer before `Serve` returns, because a client closes stdin first and signals only if the process lingers.

After `edit_end` the session finalises once every agent ended: `edit_sandbox` and `edit_check` (result `pass`/`fail`/`skip` and a note) then go to the session that ended, and an edit without `session` goes there too — a fix, which takes it back to editing. Once a reply says the user is reviewing it (or it finished), the next edit starts a new session; an edit the editor refuses because the session is the user's is retried in a new one. `edit_wait` passes on the finalising and back-to-editing notices, in tool terms. `edit_sandbox` (with `fresh`, `drop`, `include`) asks for the current session's sandbox and never starts a session: the reply names a scratch copy of every repo the session touches, with its staged files in place, where the agent builds, tests and searches before the user reviews — the instructions' step 5. `read_file`'s header also lists the open sessions changing the file's repo (from the read's detail), so an agent can join one before it starts its own.

Each process remembers every file's version as its agent last saw it — from `read_file` and from each edit's reply (`versionIn`) — and sends it with the agent's next `edit_replace`/`edit_write` (`--expect=`), so an edit made on a stale read is caught without the agent tracking anything. `read_file`'s header also warns when the agent's own staged copy can no longer be applied (its base changed and could not be replayed).

Each server process is one agent: its id (`mcp-<start time>-<pid>`) goes on every edit, `edit_end` and `edit_status` as `--agent=`. Agents may share a session: `read_file` names the session whose staged edits it shows, and an edit that passes that session while this process has none joins it — later calls without `session` go there too. The user reviews a shared session once every agent in it sent `edit_end`; an earlier `edit_end` answers that it is waiting for the others, and `edit_wait` hears the outcome.

Sessions are automatic: one generated id per server process (`mcp-<start time>-<pid>-<n>`), started by the first edit and let go once the editor answers `edit_end` or `edit_discard`, so the next edit starts a new one. `edit_wait` without a session waits on the session that ended last. `read_file` reads as the current session (its own staged copy) and never starts one. Every reply but `read_file`'s names its session. An explicit `session` argument runs a separate change set side by side.

**Functions**

### server.go

- `Serve` — the server loop: reads one message per line (at most `maxLineBytes`, 16 MiB; a longer line is drained and answered with a parse error), answers until stdin ends or the context is done. `initialize` echoes a known protocol version (else `2025-06-18`) and carries the instructions. Bad JSON gets -32700, a request without `jsonrpc: "2.0"` or a method -32600, an unknown method -32601, a call without a known tool name -32602.
- `startCall` — runs one tool call on its goroutine, tracked in `inflight` for cancellation; its result is written only if it was not cancelled.

*Plus: `readLine`, `dispatch`, `initializeResult`, `cancel`, `reply`.*

### tools.go

- `editTools.call` — one tool call: checks the arguments (`commandFor`), resolves a relative path against the working directory, finds the editor, picks the session, builds the request (`extmsg.EditRequest`, or `EditWaitRequest` with the call's budget, default 300 s, at most 1 h), and sends it with `extmsg.SendEdit`. An `error …` reply, a refused argument or a missing editor is an `isError` result with a sentence saying how to fix it. `edit_end` waits up to 45 s, since the editor may be writing every staged file. A wait the editor answers `ok waiting`, or one that runs out first, says to call `edit_wait` again. When the user set the session aside for later (Esc → Keep for later, or Later on the ready prompt), the reply says so: the edits are still staged and unwritten, and the agent may carry on and wait again when it needs the outcome. An editor that hangs up without answering (`extmsg.ErrNoReply`, e.g. restarted mid-wait) is an `isError` result telling the agent to check `edit_status` and call again: a pending session survives the restart.
- `readText` — `read_file`'s output: a header saying where the content came from ("on disk, no staged edits", "latest staged content from edit session A, NOT on disk yet", or the caller's own staged copy), then the lines numbered like `cat -n` from `offset` for `limit` lines (default 2000; a line over 2000 characters is cut, saying so), and a footer naming the next offset when lines are left.
- `forTools` — rewrites the editor's `ntee --msg` hints into tool terms (`call edit_wait`, `read_file shows`, `pass line`, `with replace_all`), so the agent is never told to run a command it does not use.
- `sessionFor` / `sessionClosed` / `sessionOver` (with `overReply`) — the automatic session id, as described under Architecture.

*Plus: `newEditTools`, `textResult`, `noEditorText`, `stillWaitingText`, `needsPath`, `knownTool`.*

### instructions.go

- `instructions` — the teaching text sent at `initialize`: when to use the tools, the read → replace → end → wait flow (read with `read_file`, since Read, Grep and Glob see only the disk), the matching rules, and what each outcome asks of the agent.
- `toolDefinitions` — the seven tools with their JSON schemas and hints (`readOnlyHint` for read_file, status and wait; `destructiveHint` for discard).

*Plus: `objectSchema`, which adds the optional `session` property to every edit tool (`read_file` has its own schema, with `offset` and `limit`).*

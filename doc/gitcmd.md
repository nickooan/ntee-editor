# internal/gitcmd

**Introduction**

`internal/gitcmd` is the one place the editor spawns git. Nothing else in the codebase calls `exec` for git directly — `internal/filetree`'s status detection and the blame feature both go through here. The reason is a single guarantee: every invocation runs as a short-lived child with a hard deadline, so a wedged git — an NFS-mounted root, `index.lock` contention, a hung fsmonitor hook — can never hang a worker goroutine, or the UI waiting on one, indefinitely.

**Architecture**

The whole package is one small file with two public entry points funneling into one private runner. Each call builds a `git -C <root> <args...>` command under a `context.WithTimeout` deadline (`Timeout`, 10 seconds — generous against the worst legitimate cases like `status`/`blame --porcelain` on huge repos, and a var so tests can shrink it). A 2-second `WaitDelay` reclaims the stdout/stderr pipes even if a grandchild process inherited them, so a lingering hook can't keep the call alive past the kill. There is no caching and no state; every call is a fresh process.

**Functions**

### gitcmd.go

`Out` runs git with the given args against a root directory and returns stdout; stderr rides along on the `*exec.ExitError` for error reporting.

`OutIn` is `Out` with bytes supplied on the child's stdin — for `blame --contents=-`, which reads the unsaved buffer content from there.

`runIn` is the shared runner both wrap: it sets up the timeout context, the `-C root` invocation, and the `WaitDelay`, and rewrites a deadline-triggered failure into a clear "timed out after 10s" error instead of a generic kill message.

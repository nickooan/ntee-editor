# internal/opcmd

**Introduction**

The parser behind op-commands: shell command templates the user saves in the inspection dashboard and runs from the Ctrl+R overlay. A template is an ordinary shell command line with placeholders — `{$1}`, `{$2}`, … for the args the user types when running it, and `{$name}` system variables the editor fills in (currently just `{$fpath}`, the absolute path of the open file). This package turns template + values into the exact command line handed to `sh -c`.

**Architecture**

`Parse` splits the source once into a `Template`: literal segments and placeholders in order, plus `MaxArg` (the highest `{$n}`, i.e. how many args the user must supply) and `System` (the system variables used). `"{$"` always opens a placeholder, so a typo like `{$hmoe}` or an unclosed `{$1` is a save-time error instead of something passed to the shell. Everything else — `$HOME`, `awk '{print $1}'`, pipes — is literal and left for the shell.

Values are quoted at render time, not by the user: `ShellQuote` leaves a plain word alone (`master` stays `master`) and single-quotes anything else (`my file.tf` → `'my file.tf'`, `a;b` → `'a;b'`). Either way the program receives the same argv, so an arg can never split into two words or inject shell syntax by accident.

Adding a system variable means adding its name to `SystemVars` and supplying its value where the app calls `Render` (`renderOpCommandLine` in `internal/app`).

**Functions**

### opcmd.go

- `Parse` — parses a template into literal and placeholder segments. `{$0}`, `{$1x}`, empty, unclosed, and unknown-variable placeholders are errors.
- `Render` — substitutes args and system values, each through `ShellQuote`. The arg count must equal `MaxArg` exactly: too few would run a half-built command, too many usually means a forgotten quote (`needs 2 args, got 3`).
- `ShellQuote` — minimal POSIX quoting: unchanged when the value is only `[A-Za-z0-9_@%+=:,./-]`, otherwise wrapped in `'…'` with embedded `'` written as `'\''`; the empty string is `''`.
- `SplitArgs` — splits the user's arg line like sh splits words: whitespace separates, `'…'` is literal, `"…"` groups (with `\"`/`\\` escapes), a bare `\` escapes one character. Unterminated quotes are an error.

*Plus small helpers: `parsePlaceholder` (one `{$…}` body to a segment), `needsQuoting` (the safe-character test).*

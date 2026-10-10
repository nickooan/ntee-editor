# internal/opcmd

**Introduction**

The parser behind op-commands: shell command templates the user saves in the inspection dashboard and runs from the Ctrl+R overlay. A template is an ordinary shell command line with placeholders — `{$1}`, `{$2}`, … for the args the user types when running it, and `{$name}` system variables the editor fills in: `{$fpath}`, the open file's path, and `{$dpath}`, its directory (`.` for a file at the top level). Both are relative to the workspace directory (the directory the editor was opened on), which is also where commands run, so the paths resolve from the command's working directory even after Ctrl+W has rooted the editor inside a nested repo. This package turns template + values into the exact command line handed to `sh -c`.

**Architecture**

The placeholder grammar:

```
{$NAME}            NAME = 1, 2, … | fpath | dpath
{$N or WORD}       a one-word default: {$1 or master}
{$N or "TEXT"}     a quoted default: {$2 or "other word"}, may hold spaces and } (\" \\ escapes)
{$N or ""}         an empty default: the placeholder disappears when the arg is omitted
```

`Parse` splits the source once into a `Template`: literal segments and placeholders in order, plus `System` (the system variables used). A run takes between `MinArg` and `MaxArg` args:
- `MaxArg` is the highest `{$n}`.
- `MinArg` is the number of *required* indices: those used at least once without a default. `{$1} … {$1 or x}` keeps `$1` required. An index never referenced is optional.

`Bind` decides which index each typed value goes to, so a default in the middle is never forced to be typed. Take `{$1} {$2 or master} {$3} {$4 or ""}`: two arguments are required (`$1`, `$3`) and the maximum is 4.
- **Exactly `MinArg` values** fill only the required indices, in order. Everything else takes its default. `a b` → `$1=a $2=master $3=b`.
- **More than `MinArg`** fill `$1…$n` in order, and indices past `n` take their defaults. `a b c` → `$1=a $2=b $3=c`. If an index past `n` has no default, that's an error. For `{$1} {$2 or p} {$3 or q} {$4}`, `a b c` reports "$4 has no default", while `a b` (required-only) and `a b c d` both work.
- **Fewer than `MinArg`, or more than `MaxArg`**, is an error: `needs 2 args ($1, $3), got 1` or `takes at most 4 args, got 5`.

An omitted argument falls back to its default. A non-empty default is shell-quoted like a typed value. An empty default renders as *nothing*, so `git log {$1 or ""} --oneline` becomes `git log  --oneline` rather than passing an empty `''` argument. A user who explicitly types `''` still gets `''`. Defaults are only allowed on arguments: `{$fpath or x}` is an error. `"{$"` always opens a placeholder, so a typo like `{$hmoe}` or an unclosed `{$1` is a save-time error instead of something passed to the shell. Everything else — `$HOME`, `awk '{print $1}'`, pipes — is literal and left for the shell.

Values are quoted at render time, not by the user: `ShellQuote` leaves a plain word alone (`master` stays `master`) and single-quotes anything else (`my file.tf` → `'my file.tf'`, `a;b` → `'a;b'`). Either way the program receives the same argv, so an arg can never split into two words or inject shell syntax by accident.

Adding a system variable means adding its name to `SystemVars` and supplying its value in `opSystemValues` (`internal/app/keys_opmode.go`).

**Functions**

### opcmd.go

- `Parse` — parses a template into literal and placeholder segments. Placeholders it rejects:
  - `{$0}` and `{$1x}`
  - empty, unclosed or unknown-variable placeholders
  - `or` with no default, or an unterminated `"`
  - junk before the `}`, such as `{$1 master}`
- `scanPlaceholder` — the single scanner for one `{$…}`: name, an optional `or` default (bare word or quoted), then `}`. Quoted text may contain `}`. `Parse` and `Spans` both use it, so they can never disagree.
- `Bind` — assigns typed values to indices with the rules above, returning one `Binding{Index, Value, FromDefault}` per index. The UI and `Render` both use it, so the preview always matches what runs.
- `Render` — renders through `Bind`. A typed value is shell-quoted. An omitted argument uses the default written *at that placeholder*, so two `{$2 or …}` occurrences can carry different defaults. An empty default renders as nothing.
- `ArgSpecs` — one entry per argument index for the UI's `$1 required · $2 = master` line. `Optional` means "has a default"; `Default` is the first one given.
- `Spans` — every placeholder's byte range and kind (Arg / System / Invalid), found with `scanPlaceholder`. A broken one spans to its next `}`. The op-commands table colours templates from it.
- `ShellQuote` — minimal POSIX quoting: unchanged when the value is only `[A-Za-z0-9_@%+=:,./-]`, otherwise wrapped in `'…'` with embedded `'` written as `'\''`; the empty string is `''`.
- `SplitArgs` — splits the user's arg line like sh splits words: whitespace separates, `'…'` is literal, `"…"` groups (with `\"`/`\\` escapes), a bare `\` escapes one character. Unterminated quotes are an error.

### complete.go

- `Complete` powers placeholder suggestions in the op-commands editor. It finds the partial placeholder that ends at the cursor (`{`, `{$`, `$`, `{$fp`, `$2`) and returns where it starts plus matching candidates, filtered by the typed name prefix. The first candidate is always the *next* unused argument, one past the highest finished `{$n}` or `{$n or …}` in the template (found with `Spans`), so typing `{$` in a template that already uses `{$1}` offers `{$2}` first. The existing arguments and the system variables come next, with descriptions from `SystemVarDetails`. A bare `$` followed by a name that matches nothing (`$HOME`) returns no candidates, so ordinary shell variables don't trigger the menu.

*Plus small helpers: `firstDefaults`, `countError`, `Plural` ("1 arg" / "2 args"), `parsePlaceholder` (a name to a segment), `scanQuoted` (a `"…"` default), `isNameByte`, `skipSpaces`, `needsQuoting` (the safe-character test).*

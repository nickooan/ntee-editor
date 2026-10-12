# internal/opcmd

**Introduction**

The parser behind op-commands: shell command templates the user saves in the inspection dashboard and runs from the Ctrl+R overlay. A template is an ordinary shell command line with placeholders — `{$1}`, `{$2}`, … for the args the user types when running it, `{#"-P $2"}` fragments that appear only when their argument is given, and `{$name}` system variables the editor fills in: `{$fpath}`, the open file's path, and `{$dpath}`, its directory (`.` for a file at the top level). Both are relative to the workspace directory (the directory the editor was opened on), which is also where commands run, so the paths resolve from the command's working directory even after Ctrl+W has rooted the editor inside a nested repo. This package turns template + values into the exact command line handed to `sh -c`.

**Architecture**

The placeholder grammar:

```
{$NAME}            NAME = 1, 2, … | fpath | dpath
{$N or WORD}       a one-word default: {$1 or master}
{$N or "TEXT"}     a quoted default: {$2 or "other word"}, may hold spaces and } (\" \\ escapes)
{$N or ""}         an empty default: the placeholder disappears when the arg is omitted
{#"TEXT $N TEXT"}  a fragment: TEXT around the quoted value when $N is given, nothing when omitted
```

`Parse` splits the source once into a `Template`: literal segments and placeholders in order, plus `System` (the system variables used). A run takes between `MinArg` and `MaxArg` args:
- `MaxArg` is the highest `{$n}`.
- `MinArg` is the number of *required* indices: those used at least once without a default. `{$1} … {$1 or x}` keeps `$1` required. An index never referenced is optional.

`Bind` decides which index each typed value goes to, so a default in the middle is never forced to be typed. Take `{$1} {$2 or master} {$3} {$4 or ""}`: two arguments are required (`$1`, `$3`) and the maximum is 4.
- **Exactly `MinArg` values** fill only the required indices, in order. Everything else takes its default. `a b` → `$1=a $2=master $3=b`.
- **More than `MinArg`** fill `$1…$n` in order, and indices past `n` take their defaults. `a b c` → `$1=a $2=b $3=c`. If an index past `n` has no default, that's an error. For `{$1} {$2 or p} {$3 or q} {$4}`, `a b c` reports "$4 has no default", while `a b` (required-only) and `a b c d` both work.
- **Fewer than `MinArg`, or more than `MaxArg`**, is an error: `needs 2 args ($1, $3), got 1` or `takes at most 4 args, got 5 — put extra flags after --`.

**Fragments** solve the flag that should vanish with its value: `go test {#"-p $1"} ./...` runs `go test -p 4 ./...` for `4` and `go test  ./...` for nothing, where `-p {$1 or ""}` would leave a dangling `-p`. Only the exact `{#"` opens one, so shell's `${#var}` stays literal. The body holds exactly one `$N`, and everything else in it must be characters `ShellQuote` leaves bare (`[A-Za-z0-9_@%+=:,./-]`) or spaces. So the body can't reopen quoting around the value or add shell syntax, and a glued form like `--parallel=$1` still renders one word (`--parallel='a b'`). A fragment counts as an argument with an empty default, so `MinArg`/`Bind` treat it like `{$N or ""}`; the same index used bare elsewhere stays required. It takes no `or`, since `-P {$2 or 4}` already covers a flag with a default.

**Extra args after `--`.** In the arg line, the first unquoted `--` ends positional binding, as in `npm run x -- …`. The words after it are shell-quoted one by one and appended to the end of the command, so `10 -- -P 30` binds `$1=10` and adds ` -P 30`. They never count toward `MinArg`/`MaxArg`, which makes them the way to pass optional flags a template doesn't name. A quoted `'--'` is an ordinary value. Extras always go at the very end, after any pipe in the template.

An omitted argument falls back to its default. A non-empty default is shell-quoted like a typed value. An empty default renders as *nothing*, so `git log {$1 or ""} --oneline` becomes `git log  --oneline` rather than passing an empty `''` argument. A user who explicitly types `''` still gets `''`. Defaults are only allowed on arguments: `{$fpath or x}` is an error. `"{$"` always opens a placeholder, so a typo like `{$hmoe}` or an unclosed `{$1` is a save-time error instead of something passed to the shell. Everything else — `$HOME`, `awk '{print $1}'`, pipes — is literal and left for the shell.

Because `Render` quotes each value as a standalone word, a placeholder must sit bare in the template. `Parse` rejects one written inside the template's own quotes (`"{$1}"`, `'{$1}'`, `$'{$1}'`) or glued to a preceding `\` or `$` (`\{$1}`, `${$1}`). Inside `"…"` the added `'…'` would be literal characters, so a value like `$(cmd)` — or a crafted file name reaching `{$fpath}` — would run. A `\` would escape the opening quote, and a `$` would turn it into a `$'…'` string that interprets backslashes in the value. The check is deliberately conservative: a placeholder inside `"$(… {$1} …)"` is technically safe but still rejected, and `#` comments and heredocs are not interpreted.

Values are quoted at render time, not by the user: `ShellQuote` leaves a plain word alone (`master` stays `master`) and single-quotes anything else (`my file.tf` → `'my file.tf'`, `a;b` → `'a;b'`). Either way the program receives the same argv, so an arg can never split into two words or inject shell syntax by accident.

Adding a system variable means adding its name to `SystemVars` and supplying its value in `opSystemValues` (`internal/app/keys_opmode.go`).

**Functions**

### opcmd.go

- `Parse` — parses a template into literal and placeholder segments. Placeholders it rejects:
  - `{$0}` and `{$1x}`
  - empty, unclosed or unknown-variable placeholders
  - `or` with no default, or an unterminated `"`
  - junk before the `}`, such as `{$1 master}`
  - a placeholder inside quotes or right after `\` / `$` (see above)
  - a fragment with no `$N`, more than one, a `$VAR`, an unsafe character, `{$2}` inside it, or an `or`
- `scanPlaceholder` — the single scanner for one placeholder: for `{$…}`, the name, an optional `or` default (bare word or quoted), then `}`. Quoted text may contain `}`. A `{#"` hands off to `scanFragment`. `Parse` and `Spans` both use it (and `nextPlaceholder` to find the next one), so they can never disagree.
- `scanFragment` — reads `{#"…"}`, checks the body (one `$N`, safe characters otherwise) and splits it into prefix, `$N` reference and suffix.
- `Bind` — assigns typed values to indices with the rules above, returning one `Binding{Index, Value, FromDefault}` per index. The UI and `Render` both use it, so the preview always matches what runs.
- `Render(args, extra, system)` — renders through `Bind`. A typed value is shell-quoted, and a fragment wraps it in its prefix and suffix. An omitted argument uses the default written *at that placeholder*, so two `{$2 or …}` occurrences can carry different defaults. An empty default, or an omitted fragment, renders as nothing. `extra` is appended at the end, each word quoted.
- `ArgSpecs` — one entry per argument index for the UI's `$1 required · $2 = master` line. `Optional` means "has a default"; `Default` is the first one given; `Fragment` is the first `{#"…"}` body using the index (`-P $2`).
- `Spans` — every placeholder's byte range and kind (Arg / System / Fragment / Invalid), found with `scanPlaceholder`. A Fragment span also carries `ReferenceStart`/`ReferenceEnd`, the range of its `$N`. A broken one spans to its next `}`, and a well-formed one in a quoted or glued position is Invalid too. The op-commands table colours templates from it.
- `ShellQuote` — minimal POSIX quoting: unchanged when the value is only `[A-Za-z0-9_@%+=:,./-]`, otherwise wrapped in `'…'` with embedded `'` written as `'\''`; the empty string is `''`.
- `SplitArgs` — splits the user's arg line like sh splits words: whitespace separates, `'…'` is literal, `"…"` groups (with `\"`/`\\` escapes), a bare `\` escapes one character. Unterminated quotes are an error. It returns the args and the extras: the words after the first `--` written without quotes or escapes.

### filetypes.go

An op-command's TYPE says which files Ctrl+R offers it for: a comma list of file types, or `*` for all.
- `NormalizeFileTypes` — the stored form: items trimmed, a leading `.` dropped, lowercased, de-duplicated and joined with `,` (`" .Go , rb"` → `go,rb`). An empty spec, or any `*`, is `*`. An item must be `[a-z0-9._+-]`.
- `MatchFileType(types, fileName)` — whether a spec applies to a file, and whether it did so *specifically* (by naming the type) rather than through `*`. An item matches the file's extension (`prod.tfvars` → `tfvars`, not `tf`) or its whole base name, for extensionless files like `Dockerfile`. `FileTypeOf` is that type for one file.
- `FileTypesOverlap` — whether two specs claim a common file, which is what makes two commands with the same name clash. `*` overlaps only `*`: a specific type is meant to override it.
- `CompleteFileType` — suggestions for the comma item at the cursor, from `BuiltinFileTypes` (go, rb, tf, tfvars, sh, …). The open file's type comes first, an exact match of the typed text ranks above everything, types already listed are skipped, and `*` is offered only while the rest of the spec is empty.

### complete.go

- `Complete` powers placeholder suggestions in the op-commands editor. It finds the partial placeholder that ends at the cursor (`{`, `{$`, `$`, `{$fp`, `$2`) and returns where it starts plus matching candidates, filtered by the typed name prefix. The first candidate is always the *next* unused argument, one past the highest finished `{$n}`, `{$n or …}` or `{#"… $n"}` in the template (found with `Spans`), so typing `{$` in a template that already uses `{$1}` offers `{$2}` first. The existing arguments and the system variables come next, with descriptions from `SystemVarDetails`. A bare `$` followed by a name that matches nothing (`$HOME`) returns no candidates, so ordinary shell variables don't trigger the menu.

- `quoteTracker` — follows sh quoting (`'…'`, `"…"`, `$'…'`, backslash escapes) through the literal text between placeholders. `advance` consumes one literal, and `check` returns the error for a placeholder at the current position. `Parse` and `Spans` both run it, so they agree.

*Plus small helpers: `nextPlaceholder` (the next `{$` or `{#"`), `firstDefaults`, `countError`, `Plural` ("1 arg" / "2 args"), `parsePlaceholder` (a name to a segment), `scanQuoted` (a `"…"` default), `isNameByte`, `skipSpaces`, `needsQuoting` (the safe-character test).*

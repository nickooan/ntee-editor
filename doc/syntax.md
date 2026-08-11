# internal/syntax

**Introduction**

This package turns file content into colors. It wraps [chroma](https://github.com/alecthomas/chroma) tokenization and produces per-line `view.HighlightSegment` slices, which the app's renderers (`internal/app`, plus the OpenAPI and GraphQL preview renderers) turn into styled terminal output.

One constraint shapes everything here: tokenization is always whole-buffer. Chroma is stateful across lines — block comments, template literals, GraphQL selection sets — so lexing a single line in isolation would mis-color multi-line constructs. Callers hand in the full content and get back one segment list per line.

The package also ships two hand-written chroma lexers for ntee-r1quest's own languages, `.nts` (request scripts) and `.ntd` (data files), since no built-in lexer knows them.

**Architecture**

The pipeline is short: filename → lexer → tokens → per-line segments.

1. `LexerFor` picks a lexer by extension: custom ntee lexers first, then a pinned list of first-class languages, then chroma's filename matcher. No match means "render plain".
2. `HighlightLines` tokenizes the whole content and splits multi-line tokens across line buckets.
3. `segmentFor` maps each token type to a color/attribute combo drawn from the active chroma style (gruvbox by default, tuned), cached per token type.

File map:

- `syntax.go` — lexer resolution and the tokenize-and-bucket entry point.
- `theme.go` — active chroma style, style selection, token-type → segment mapping.
- `ntee.go` — lexical fragments shared by the two ntee-r1quest languages (comments, macros, strings, primitives).
- `nts.go` — lexer for `.nts` request scripts.
- `ntd.go` — lexer for `.ntd` data files, including their GraphQL sugar.

**Functions**

### syntax.go

- `LexerFor(filename)` — resolves the lexer for a filename: custom `.nts`/`.ntd` lexers, then the `explicitLexers` extension map (Go, TypeScript, YAML, bash, GraphQL, ...), then chroma's own filename matcher. Returns nil when the file should render unstyled. Everything is wrapped in `chroma.Coalesce` so adjacent same-type tokens merge. Results are memoized in a mutex-guarded map (chroma's registry walk is expensive and this runs on every full re-highlight) — keyed by extension for the pinned tables, by basename for the registry fallback (chroma globs can match specific basenames like `CMakeLists.txt`).
- `HighlightLines(filename, content)` — the main entry point. Normalizes line breaks first (chroma's `EnsureLF` converts a lone `\r` to `\n`, so counting the raw content would desync rows), then tokenizes the whole buffer and buckets styled segments by line, splitting only the tokens that actually contain newlines. The result always has exactly as many rows as `view.NormalizeLines(content)` would produce; nil means "no lexer, render plain".

### theme.go

- `SetStyle(name)` — selects the chroma style grammar colors come from. Unknown names and the legacy `"terminal16"` fall back to the tuned gruvbox. An `init` seeds gruvbox as the default; the inspect pane can switch styles at runtime. The style and segment cache are mutex-guarded — highlighting also runs on `tea.Cmd` goroutines (grep and def-picker previews). Also resets the segment cache.
- `gruvboxTuned()` — derives chroma's gruvbox with red keywords/operators instead of orange, matching the classic vim/Sublime gruvbox look while keeping types gold.
- `segmentFor(t)` — maps a chroma token type to a `view.HighlightSegment` (hex color, bold/italic/underline), memoized in `entryCache`. `Style.Get` resolves category inheritance, so every token type lands on a concrete color.

### ntee.go

- `nteeSharedRules()` — the lexical fragments both ntee languages share: `//` comments, JSON-ish primitives, `@name(...)` macros with `or` defaults, and double-quoted strings that may span lines and interpolate macros. One subtlety documented in the source: `\n` must be its own token, because chroma anchors patterns at the current position and a greedy `\s+` would swallow the next line start and break `^`-anchored keyword rules.

*Plus a small helper: `rule` — builds a keyed `chroma.Rule`, since chroma's unkeyed literal style trips go vet outside chroma itself.*

### nts.go

- `ntsRules()` — rules for `.nts` request scripts. Statement keywords (`type`, `ref`, `url`, `header`, `auth`, `body`, ...) are reserved only at statement heads; inside `body { }` the same words are ordinary object keys, so the body state deliberately carries no keyword rules. `type` also colors the following HTTP-method token and `ref` the `.ntd` path.

### ntd.go

- `ntdRules()` — rules for `.ntd` data files: `key: value` entries plus the `query`/`mutation` GraphQL sugar. The GraphQL selection set is the only place `#` starts a comment, and `keyColon` is matched before `gqlStart` so a key literally named `query:` still lexes as a key.

*The lexer values themselves (`ntsLexer`, `ntdLexer`) are just `chroma.MustNewLexer` wiring around these rule functions.*

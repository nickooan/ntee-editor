# ntee-editor — guidelines for AI-assisted changes

ntee-editor is a Sublime-style TUI text editor built on Bubble Tea. Architecture documentation for every `internal/` package lives in [doc/](doc/) — start with [doc/README.md](doc/README.md) for the package map and data flow.

## Naming

- Use full, descriptive names for functions, methods, variables, and arguments. Prefer `resolveProjectRoot` over `resolveRoot`, `candidateIndex` over `ci`, `previousRevision` over `prevRev`.
- Avoid abbreviations and shortcuts that force the reader to guess. Short names are acceptable only for tiny, conventional scopes (a loop index, a receiver).

## Documentation (doc/) must stay in sync

- When refactoring, renaming, or changing the behavior of a function, check the package's file under [doc/](doc/) and update any description that mentions it.
- When adding a new function or feature, add it to the correct `doc/<package>.md` — important functions get a short plain-language explanation of what they do and why; trivial helpers go into the file's italic roll-up line.
- A change that adds a package needs a new `doc/<package>.md` (Introduction / Architecture / Functions template) plus a row in `doc/README.md`'s table.

## Code comments

- Keep comments sparse. The high-level "what this does and why it exists" story belongs in [doc/](doc/), not repeated inline — don't append comments that restate the code or duplicate the docs.
- Comment only the genuinely important things the code can't say itself: a non-obvious invariant, a concurrency/lock-ordering rule, a deliberate trade-off, or a subtle edge case that would trip the next reader.

## Performance and user convenience

- Every editor change must consider both. Nothing may block the UI goroutine: slow work (file walks, git, LSP, parsing/rendering) runs on `tea.Cmd` goroutines and lands back as messages guarded by generation counters.
- Watch per-keystroke and per-frame costs — memoize (see `matchCache`/`frameCache` in `internal/app`) rather than recompute, and avoid allocations in hot loops (`internal/fuzzy` is the reference for allocation discipline).
- Prefer behavior that feels right to the user: predictable ordering, confirmation before destructive actions, errors surfaced in the status bar instead of swallowed.

## Tests

- Unit test coverage and correctness are always part of the change, not an afterthought. New behavior gets a test; changed behavior gets its pinned tests updated deliberately (never weakened just to pass).
- Verify before finishing: `go build ./... && go vet ./... && gofmt -l .` (must be empty) and `go test ./...`. For concurrency-adjacent changes (`internal/lsp`, `internal/app`) also run `go test -race`.

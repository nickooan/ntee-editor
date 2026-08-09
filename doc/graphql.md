# internal/graphql

**Introduction**

This package turns a set of GraphQL SDL files into a read-only, styled schema document. It powers the GraphQL preview mode in `internal/app`: you open a `.graphql` file, the app asks this package to gather the whole schema file set, parse and merge it, and render it as browsable lines with an outline sidebar. Every rendered row carries a `{file, line}` source anchor, so pressing Esc jumps back to the exact spot in the source file.

The package is deliberately pure: no filesystem access, no UI framework. All IO is injected by the caller through `GatherIO`. Unlike its sibling `internal/openapi`, there is no `$ref` machinery here — the GraphQL ecosystem convention is to concatenate every SDL file and merge, with `extend type` as the native cross-file mechanism. Every type reference is one hop to a sibling top-level block, so there are no depth budgets or cycle guards either.

**Architecture**

The pipeline runs in three stages, one file each:

1. **Gather** (`gather.go`) — figure out *which* files make up the schema. If a GraphQL Config file (`.graphqlrc.yml` and friends) is found walking up from the open file, its `schema:` globs win; otherwise every SDL file in the open file's directory is merged. The open file is always included.
2. **Parse** (`schema.go`) — `ParseFiles` parses each file independently with gqlparser and merges the results into a `Schema`: a list of `*Def` (one per named type, base definition plus its extensions from any file), directive definitions, and the resolved operation `Roots`. One broken file becomes a `FileError`, not a total failure.
3. **Render** (`render.go`) — `RenderSchema` walks the merged schema and emits `Line` values (styled segments plus a `SourceRef`) and `OutlineEntry` values (sidebar rows pointing into the rendered lines).

The key types:

- `Line` / `SourceRef` — one rendered row and where it came from. Rows without their own source (blanks, closing braces) inherit the previous row's, so Esc always lands somewhere sensible.
- `OutlineEntry` (`outline.go`) — a sidebar row: a group header (depth 0) or a root field / type definition (depth 1), anchored by index into the rendered lines.
- `Schema` / `Def` / `Roots` — the merged multi-file schema. Each `Def` keeps its ast nodes, and every ast node carries its own file position, so no per-file bookkeeping is needed.
- `GatherIO` — the injected filesystem surface (`ReadFile`, `ListDir`, `ListAll`), all workspace-root relative.

**Functions**

### detect.go

- `Detect` — reports whether a filename is a GraphQL SDL file, purely by extension (`.graphql`, `.gql`, `.graphqls`). It's the pre-flight guard behind the "not a graphql file" alert when entering the mode from the @exec bar.

### gather.go

- `Gather` — the entry point: returns the schema file set for the open file, the config file that drove it (if any), and notes about anything skipped. Config globs win when a config exists; same-directory SDL files otherwise.
- `findConfig` — walks up from the open file's directory to the workspace root, probing each directory for one of the four graphql-config filenames. It checks existence via `ListDir` rather than trying to read, because `ReadFile` may fold read errors into content.
- `gatherByConfig` — matches the config's `schema:` globs against the recursive workspace file list, keeping that list's order. That ordering also dedupes files matched by overlapping patterns for free.
- `configPatterns` — pulls the glob patterns out of the config YAML. The `schema:` field can be a string or a list; URL entries and mapping forms (the headers variant) are skipped with a note instead of failing.
- `gatherSameDir` — the no-config default: every SDL file in the open file's own directory.
- `matchGlob` / `matchSegs` — a slash-segment glob matcher where `**` spans zero or more whole segments and everything else goes through `path.Match`. Brace patterns are unsupported and simply match nothing, which surfaces as an empty gather rather than a silent subset.

*Plus small helpers: `ensureIncluded` (the open file is always in the set), `dirOf` and `joinRel` (root-relative path plumbing where `""` means the workspace root).*

### schema.go

- `ParseFiles` — parses each `NamedSource` independently and merges into one `Schema`. Parsing per file means one broken file becomes a `FileError` instead of killing the schema, and declaration order survives (the validator's map form would lose it). Merging handles the tricky cases: a name first seen as `extend` gets its base backfilled later (file order must not decide which block is "real"), duplicate base definitions are demoted to extensions with a note, and a base-less `extend` still gets a `Def` so it renders. It also resolves the operation roots from `schema {}` blocks, falling back to the conventional `Query`/`Mutation`/`Subscription` names when such a type actually exists.
- `IsDefined` — whether a type name resolves in the merged schema, including the five spec builtins. This is the "one hop" replacement for OpenAPI's `$ref` resolution; the renderer uses it to tag unresolved types inline.
- `Lookup` — returns the merged `Def` for a name, nil when undefined.
- `fileError` — shapes a gqlparser error into a `FileError` with a line number, unwrapping via `errors.As` both as a single error and a list because gqlparser's concrete return type changed across v2.5.x.

### outline.go

- `KindColor` — the ANSI color name for an outline kind badge (`query` is green, `mutation` yellow, and so on), for callers styling sidebar rows outside the package.
- `KindBadge` — the short badge text (`qry`, `mut`, `sub`, …); the full word "subscription" would eat the narrow outline pane.

### render.go

- `RenderSchema` — renders the whole merged schema as one continuous document: title, parse errors, then the three operation root groups (fields rendered like OpenAPI operations), then named types grouped by kind (Types, Interfaces, Inputs, Enums, Unions, Scalars) in declaration order, then directives. Width only drives the group-header fill; the caller's viewport truncates long rows.
- `emit` — the one place lines are appended; a zero `SourceRef` inherits the previous row's, which is how blanks and closing braces stay anchored.
- `posRef` / `defSrc` — convert gqlparser positions into `SourceRef`s. `defSrc` anchors a merged def at its base's position, falling back to the first extension.
- `defFields` — collects a def's fields base-first then extension by extension, each field keeping its own file anchor.
- `rootGroup` / `rootField` / `rootFieldHead` / `argRows` — the operation-style rendering for root fields: a bold head with args and return type, with arguments breaking out one per row when there are more than two or the head would overflow the width. Deprecated fields render struck-through with the reason.
- `typeGroup` — filters `Defs` by kind, emits the group header, and dispatches each def to the right renderer.
- `defHead` — the shared "keyword Name … {" head row. A base-less def (only `extend` blocks anywhere) renders dim with a "(base not defined)" marker; extension counts and merge notes append as a dim tail.
- `objectDef` / `fieldRow` — the block form for objects, interfaces, and inputs: `name(args): Type` per row with dim annotations (default, unresolved type, deprecated, description) inline.
- `enumDef` — enum values one per row, capped at 64 (`maxEnumValues`) with an "…+N" collapse row for giant country/locale enums.
- `unionDef` — union members one per row, each anchored to its own type position when the parser recorded one.
- `directiveGroup` — directive definitions with their args and `on LOCATION | …` tail.
- `deprecation` — reads the `@deprecated` directive and its `reason` argument.

*Plus small helpers: `Plain` (flatten lines to text for search and tests), `seg`, `dim`, `sp`, `boldSeg`, `plainLen` (segment builders and width math), `blank`, `title`, `parseErrors` (each broken file becomes an inline red row anchored to the broken spot), `groupHeader`, `scalarDef`, `extCount`, `inlineArgs`, `deprecatedTail`, `firstLine`, `padTo`, `truncRunes`.*

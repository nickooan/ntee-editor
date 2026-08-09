# internal/openapi

**Introduction**

This package parses OpenAPI v3 YAML documents and renders them as styled terminal lines. It powers the OpenAPI preview mode in `internal/app`: open a spec, and the app renders it as a read-only, Swagger-UI-flavored document with a method/path outline in the sidebar. Every rendered row carries a `{file, line}` source anchor, so pressing Esc jumps back to the exact line in the YAML — even when that line lives in a different file reached through a `$ref`.

Like its sibling `internal/graphql`, the package is pure data-in/data-out: no UI framework, no filesystem access. File loading is injected into the `Resolver`, which resolves cross-file `$ref`s lazily as the renderer encounters them, and refuses to follow paths that escape the workspace root.

**Architecture**

Three concerns, three files:

1. **Parse** (`spec.go`) — `Parse` decodes YAML (or JSON, which is valid YAML) into a `Document`: info, tags, paths, and components. The foundation is `OrderedMap[V]`, a generic YAML mapping that preserves key order and each key's source line — Go maps lose both, and property/path order matters for readability while source lines drive the exit-to-source jump. Most structs (`Operation`, `Parameter`, `Schema`, …) have custom unmarshalers whose only job is recording their own source line.
2. **Resolve** (`resolve.go`) — `Resolver` handles `$ref`s one hop at a time. Local refs stay in the root document; file refs load and cache other files through the injected reader; external URLs and paths outside the workspace root are rejected with a reason string the renderer prints inline instead of failing.
3. **Render** (`render.go`) — `RenderDocument` walks the document and emits `Line` values (styled segments plus a `SourceRef`) and `OutlineEntry` values. Operations are grouped by their first tag; each operation renders its parameters, request body, and responses, with schemas expanded as indented trees.

The key types:

- `Line` / `SourceRef` — one rendered row and its source location. Rows without their own source (blanks, closing braces) inherit the previous row's.
- `OutlineEntry` (`outline.go`) — a sidebar row: a tag group header (depth 0) or an operation (depth 1) with its method and path, anchored by index into the rendered lines.
- `Document` and friends — the rendered subset of the spec. Not a full OpenAPI model; just what the preview shows.
- `Resolver` — the jailed, caching, lazy ref resolver.

Schema trees are kept safe by three guards: a per-tree line budget (`maxSchemaLines` = 200), a depth cap (`maxSchemaDepth` = 8), and a `seen` set keyed by normalized ref identity so recursive schemas render as `↩ Name (recursive)` instead of looping.

**Functions**

### spec.go

- `OrderedMap.UnmarshalYAML` — decodes a mapping node while recording key order and each key's line. This is what makes both "render in author order" and "Esc jumps to the right line" possible.
- `Detect` — cheap check that content looks like an OpenAPI v3 document: a regex for the `openapi: 3` key over the first 50 lines, fast enough to run synchronously. This is what produces the "not a valid OpenAPI file" alert.
- `Parse` — decodes a whole document. It deliberately does not enforce the `openapi:` version field, because referenced files may be bare components files.
- `ParseSchema` — decodes a file whose entire content is a single schema (the bare-file `$ref: ./pet.yaml` form).
- `PathItem.UnmarshalYAML` — besides decoding, it records `MethodLines`: the source line of each `get:`/`post:` key. A YAML mapping node's own line is its first child's, so the operation heading needs the key's line captured separately to anchor correctly.
- `PathItem.Operations` — returns the item's present operations in canonical method order (GET, POST, PUT, PATCH, DELETE, OPTIONS, HEAD, TRACE).
- `AdditionalProps.UnmarshalYAML` — handles `additionalProperties` being either a bool or a schema.

*Plus small helpers: `OrderedMap.Get`/`Len`/`Line`, `kindName`, and the line-recording unmarshalers on `Operation`, `Parameter`, `RequestBody`, `MediaType`, `Response`, and `Schema` — all the same alias-decode-then-stamp-the-line pattern.*

### resolve.go

- `NewResolver` — creates a resolver around an injected `load` function that reads workspace-root-relative files. Loaded documents, bare-file schemas, and failures are all cached, so a bad ref only costs one read attempt.
- `document` — loads and caches the document a ref points into. Local refs return the current document; external URLs and paths that resolve to `../` are rejected with a reason (jailing to the root is primarily the caller's job, but escapes are rejected here too).
- `ResolveSchema` — resolves a schema ref one hop. Handles both the `#/components/schemas/Name` pointer form and the bare-file form where the whole file is one schema. On success the returned document owns the schema, so its `File` locates the source for anchoring.
- `ResolveParameter` / `ResolveResponse` / `ResolveRequestBody` — the same one-hop resolution for the other three component kinds, all funneling through the shared `component` helper.
- `RefKey` — normalizes a ref to `cleanedPath#pointer`, the stable identity the renderer's cycle guard uses across files.
- `RefName` — the display name of a ref: the last pointer segment, or the file's base name for bare-file refs.

*Plus small helpers: `splitRef` (split `file#/pointer`), `unescapePointer` (JSON-pointer `~1`/`~0`), `isExternalURL`, `normPath` (resolve a file ref relative to the referencing document's directory), `componentRef` (split a `/components/<kind>/<name>` pointer).*

### outline.go

The whole file is the `OutlineEntry` type — no functions. `Method` and `Path` are empty for tag headers; `LineIdx` points into the rendered lines.

### render.go

- `RenderDocument` — the entry point: title block, then each tag group with its operations, as one continuous document. Width only drives right-alignment and separator fills; the caller's viewport truncates long rows.
- `groupByTag` — buckets operations by their *first* tag: declared tag order first (only tags actually used), then undeclared tags in encounter order, untagged operations last under an "other" group. Group headers are skipped entirely when there's just one unnamed group.
- `operation` — renders one operation: the bold method + path head (with the operationId right-aligned and deprecated paths struck through), summary and description, then parameters, request body, and responses. The head anchors on the `get:`/`post:` key line via `MethodLines`, not the operation body's first line. Path-level parameters are merged in ahead of operation-level ones.
- `parameters` — resolves any `$ref` parameters, then renders an aligned table: name (with a red `*` for required), location, type label, description. Unresolved refs become an inline dim row instead of breaking the table.
- `requestBody` / `responses` — render the body per content type and each response per status code (status colored by class), resolving component refs first and expanding each media type's schema as a tree.
- `emitS` — `emit` for schema-tree rows: it spends the current tree's line budget and collapses everything past it into a single "… truncated" marker.
- `schemaRoot` — starts a fresh schema tree with its own budget and cycle set. An inline plain object renders its properties directly (no redundant `object {` head); refs and other shapes get a head line.
- `schemaTree` — renders one schema node: enforces the depth cap, resolves `$ref`s through the resolver, and uses the `seen` set to render recursion as `↩ Name (recursive)`. The seen key is removed after the subtree, so the same ref can appear in sibling branches.
- `schemaNode` — dispatches a resolved schema to the right shape: `allOf`, `oneOf`/`anyOf`, array, object, or scalar, each with its dim annotation tail.
- `mergeAllOf` — shallow-merges `allOf` branches: properties in first-seen order with later branches overriding, a union of `required`, sibling properties on the schema itself merging last with highest precedence. Its annotation string names ref branches and counts inline ones (e.g. `allOf: Pet + 2 inline`).
- `arrayNode` — arrays render as `array of X`, resolving item refs (with the same cycle guard) and recursing into structured item shapes.
- `objectChildren` — renders an object's properties in source order, each anchored to its key's line, plus an `<additional>` row for `additionalProperties` schemas.
- `labelFor` — the one-line type label used where trees don't expand (parameter rows): refs by name, arrays by item label.
- `appendAnnotations` — the dim annotation tail: enum (capped at 6 values), default, nullable, readOnly, writeOnly, deprecated.
- `MethodColor` — the ANSI color for an HTTP method badge (the Swagger UI convention), exported for callers styling outline rows.

*Plus small helpers: `Plain` (flatten lines to text for search and tests), `methodColor`, `statusColor`, `seg`, `dim`, `sp`, `boldSeg`, `plainLen`, `emit`, `blank`, `title`, `tagHeader`, `descLine`, `propPrefix`, `allOfNode`, `variantsNode`, `isPlainObject`, `scalarLabel`, `firstLine`, `padTo`, `truncRunes`, `starLen`.*

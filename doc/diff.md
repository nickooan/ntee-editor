# internal/diff

**Introduction**

Line-level diffing with the greedy Myers O(ND) algorithm. The package is dependency-free and pure: given two line slices, it decides *what changed* and hands back an edit script; `internal/app`'s diff mode turns that script into display rows. Two constraints matter: the search depth is bounded (so a pathological rewrite can't burn ~16MB+ of backtracking trace), and changed regions always come out Deletes-before-Inserts, the GitHub hunk shape.

**Architecture**

One file, one entry point. `Lines` interns lines to ints, trims the common prefix and suffix, runs `myers` on the middle, then `groupDelsFirst` reorders each changed run. The output is a flat `[]Op` — `Equal`, `Delete`, or `Insert`, each carrying the relevant index into the old (`AIdx`) and/or new (`BIdx`) input.

**Functions**

### diff.go

- `Lines(a, b)` — the public entry point. Interns every line so the inner loop compares ints instead of strings, trims the shared prefix and suffix (only the middle needs Myers), and stitches the three parts back into one script with Equal ops for the trimmed regions included.
- `myers(a, b, aOff, bOff)` — the greedy forward Myers search over the trimmed middle, recording the furthest-reaching frontier per depth, then backtracking that trace into an edit script. One-side-empty inputs short-circuit (the D-loop would otherwise cost O(len²) on them). If the depth bound `maxD` (1024) is exceeded — or the sides' lengths differ by more than it, so it certainly would be — it hands the middle to `anchoredDiff` instead.
- `anchoredDiff(a, b, aOff, bOff)` — the fallback for a rewrite too big for one bounded search: lines that occur exactly once on each side are matched up, the longest run of them in order on both sides becomes the anchors (`uniqueCommonAnchors`, `longestRisingInB` — patience diff's idea, O(k log k)), and each piece between anchors goes back through `myers`. Untouched lines therefore stay equal even across a big refactor; only a piece with no such line degrades to "delete everything, insert everything" (`allChanged`). Normal diffs never get here, so their output is unchanged.
- `groupDelsFirst(ops)` — reorders each maximal run of non-Equal ops so all Deletes precede all Inserts, giving callers the removed-block-above-added-block layout directly.

*Plus a small helper: `internAll` — maps lines to stable int ids via a shared table.*

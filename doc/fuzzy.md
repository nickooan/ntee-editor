# internal/fuzzy

**Introduction**

`internal/fuzzy` is a rune-based, case-insensitive subsequence matcher for file paths, in the spirit of Sublime's Goto Anything: type a few characters, get the paths that contain them in order, best-scored first. Its main consumer is `internal/filetree`'s query-bar suggestions, which run it over the whole project corpus on every keystroke.

That per-keystroke usage drives the package's one hard constraint: allocation discipline. A `Prepared` candidate holds no decoded rune data — just the original string (sharing its bytes with the caller) and one int — so a finder held open over a large workspace costs a few MB instead of tens of MB of `[]rune` copies. Scoring case-folds runes on the fly, the pre-filter allocates nothing, and matched-position slices for bold rendering are only computed for the handful of visible rows.

Scoring rewards what people actually mean: hits on word boundaries (after `/`, `_`, `-`, `.`, space, or a lowercase→uppercase transition), consecutive runs, and matches concentrated in the basename; gaps are penalized and shorter candidates win ties. One deliberate ranking override sits on top: when the query is a literal directory prefix (`internal/app/`), the children of that directory list first, alphabetically — the user is browsing a directory, not asking for scattered subsequence matches.

**Architecture**

Everything lives in one file. The flow is: `Prepare` once when the finder opens, then `Filter` per keystroke, then `Positions` per visible row.

`Prepare` wraps each candidate as a `Prepared`, recording where the basename starts (trailing-`/` directory candidates anchor on their last real segment so basename bonuses still apply). `Filter` runs two passes per candidate: `isSubsequence`, a single linear allocation-free scan that rejects the majority of the corpus cheaply, then `align`, the real scorer, only for survivors. Matches come back as `{Index, Score}` pairs — no per-match position slice — sorted by score, then candidate length, then `orderDirPrefix`'s directory-browse reordering.

`align` exists because pure greedy forward matching picks bad alignments ("tree" scattering across "in**t**_e_rnal" instead of landing on "keys_**tree**"). It greedily matches from each occurrence of the first query rune — capped at `maxStarts` (16) — and keeps the best-scoring alignment, delegating per-alignment scoring to `scoreFrom`. The same routine serves both `Filter` (no position tracking, zero allocation) and `Positions` (with it).

**Functions**

### fuzzy.go

`Prepare` wraps candidates for repeated matching, preserving order so a `Match.Index` still points into the caller's original slice, and computes each candidate's basename start.

`Filter` returns the candidates matching the query as a case-folded subsequence, best score first; an empty query matches everything at score 0 in original order. It's the per-keystroke hot path: cheap subsequence reject, then the multi-start scorer, then a stable sort (score, then shorter-path tiebreak), then the directory-browse reorder.

`orderDirPrefix` implements the directory-browse rule. When the query contains a `/`, candidates under that literal prefix partition to the front; with no typed tail (query ends in `/`) they sort alphabetically, and with a tail the in-class order stays score-ranked. Without this, every child of a directory scores identically and the listing degrades to path-length order.

`isSubsequence` is the fast pre-filter: one linear pass checking that the query's runes appear in order, no allocation, no scoring.

`align` finds the highest-scoring alignment of the query by trying a greedy match from each occurrence of the first query rune (up to 16 starts) and keeping the best. It adds a +4 bonus for alignments starting in the basename, nudges down long candidates (`- len/8`), and only tracks positions when the caller asks — the `Filter` path allocates nothing.

`scoreFrom` scores one greedy alignment: +2 per hit, +3 on a word boundary, +2 for extending a consecutive run, minus a gap penalty capped at 3 so one long gap can't drown the bonuses. Returns `ok=false` if the query isn't fully consumed.

`Positions` recomputes the best alignment's matched rune indices for bold rendering — meant only for the visible rows, since it redoes the work `Filter` already scored.

*Plus small helpers: `foldRune` — per-rune lower-casing with an ASCII fast path for the hot loop; `isBoundary` — the word-start test (string start, after a separator, or lower→Upper camelCase transition).*

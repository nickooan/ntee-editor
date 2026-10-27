package app

import (
	"bytes"
	"sort"
	"unicode/utf8"

	"github.com/nickooan/ntee-editor/internal/diff"
	"github.com/nickooan/ntee-editor/internal/gitcmd"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/view"
)

// diffRowKind classifies one display row of a diff document.
type diffRowKind uint8

const (
	diffCtx diffRowKind = iota // unchanged: normal background, numbered
	diffAdd                    // added: green background, numbered
	diffDel                    // removed: red background, blank gutter
)

// diffRow is one rendered line of the unified diff. Ctx/add rows point into
// the document's new-side lines (no text duplication); del rows carry the
// removed text and point at the new-side line the deletion sits before (its
// successor, clamped), which is what Esc and Ctrl+J map a del row to.
// bufLine is non-decreasing down the slice, so line lookups can binary-search.
type diffRow struct {
	kind    diffRowKind
	bufLine int
	text    string // del rows only
}

// diffDocument is one file's unified diff, shared by git diff mode (over the
// live edit buffer) and review mode (one per reviewed file). lines is the
// new side; highlights, when present, is its per-line syntax cache.
type diffDocument struct {
	rows        []diffRow
	lines       []string
	highlights  [][]view.HighlightSegment
	adds, dels  int
	newFile     bool // the base has no such path: every row is an add
	deletedFile bool // the working tree has no such path: every row is a del
}

func (doc diffDocument) rowText(i int) string {
	if len(doc.rows) == 0 {
		return ""
	}
	row := doc.rows[input.Clamp(i, 0, len(doc.rows)-1)]
	if row.kind == diffDel {
		return row.text
	}
	if row.bufLine >= 0 && row.bufLine < len(doc.lines) {
		return doc.lines[row.bufLine]
	}
	return ""
}

// rowHighlights is row i's syntax segments, nil for del rows and rows past
// the highlight cache.
func (doc diffDocument) rowHighlights(i int) []view.HighlightSegment {
	row := doc.rows[i]
	if row.kind == diffDel || row.bufLine < 0 || row.bufLine >= len(doc.highlights) {
		return nil
	}
	return doc.highlights[row.bufLine]
}

func (doc diffDocument) rowWidth(i int) int {
	return utf8.RuneCountInString(doc.rowText(i))
}

// isHunkStart reports whether row i opens a hunk: a maximal run of non-ctx rows.
func (doc diffDocument) isHunkStart(i int) bool {
	return doc.rows[i].kind != diffCtx && (i == 0 || doc.rows[i-1].kind == diffCtx)
}

// firstChangeRow is the first hunk's row, 0 when the document has none.
func (doc diffDocument) firstChangeRow() int {
	for i := range doc.rows {
		if doc.rows[i].kind != diffCtx {
			return i
		}
	}
	return 0
}

func (doc diffDocument) span(height int) rowSpan {
	return rowSpan{total: len(doc.rows), height: height, rowWidth: doc.rowWidth}
}

// buildDiffRows converts the Myers edit script into display rows and fills
// each del row's bufLine with its successor ctx/add row's line (clamped at
// EOF), walking backwards once.
func buildDiffRows(old, cur []string) (rows []diffRow, adds, dels int) {
	ops := diff.Lines(old, cur)
	rows = make([]diffRow, 0, len(ops))
	for _, op := range ops {
		switch op.Kind {
		case diff.Equal:
			rows = append(rows, diffRow{kind: diffCtx, bufLine: op.BIdx})
		case diff.Insert:
			rows = append(rows, diffRow{kind: diffAdd, bufLine: op.BIdx})
			adds++
		case diff.Delete:
			rows = append(rows, diffRow{kind: diffDel, bufLine: -1, text: old[op.AIdx]})
			dels++
		}
	}
	next := max(0, len(cur)-1)
	for i := len(rows) - 1; i >= 0; i-- {
		if rows[i].kind == diffDel {
			rows[i].bufLine = next
		} else {
			next = rows[i].bufLine
		}
	}
	return rows, adds, dels
}

// diffRowBufLine maps a display row to its new-side line (del rows map to
// their successor), clamped so callers can feed any cursor value.
func diffRowBufLine(rows []diffRow, i int) int {
	if len(rows) == 0 {
		return 0
	}
	return rows[input.Clamp(i, 0, len(rows)-1)].bufLine
}

// diffRowForBufLine maps a new-side line to its ctx/add display row. Every
// such line has exactly one such row, and bufLine is non-decreasing, so a
// binary search plus a skip over the preceding del run lands exactly.
func diffRowForBufLine(rows []diffRow, line int) int {
	if len(rows) == 0 {
		return 0
	}
	i := sort.Search(len(rows), func(i int) bool { return rows[i].bufLine >= line })
	for i < len(rows) && rows[i].kind == diffDel {
		i++
	}
	return input.Clamp(i, 0, len(rows)-1)
}

// computeDiffDocument diffs current (the new side; currentExists false means
// the path is gone from the working tree) against base:repoRel in the repo at
// repoRoot. base "" means HEAD, and a HEAD that doesn't resolve (a repo with
// no commits yet) is not an error — everything is new. It runs git, so call
// it off the UI goroutine. The error is user-facing, without a mode prefix.
func computeDiffDocument(repoRoot, base, repoRel string, current []string, currentExists bool) (diffDocument, string) {
	revision := resolveDiffRevision(repoRoot, base)
	if revision.problem != "" {
		return diffDocument{}, revision.problem
	}
	return diffAgainstRevision(repoRoot, revision, repoRel, current, currentExists)
}

// diffRevision is base resolved in one repo: name is what git show reads
// from, and exists is false for an unborn HEAD (everything is new).
type diffRevision struct {
	name    string
	exists  bool
	problem string
}

// resolveDiffRevision checks base in the repo at repoRoot once, so a review
// can share it across every file of that repo. It runs git.
func resolveDiffRevision(repoRoot, base string) diffRevision {
	revision := diffRevision{name: base, exists: true}
	if revision.name == "" {
		revision.name = "HEAD"
	}
	if _, err := gitcmd.Out(repoRoot, "rev-parse", "--verify", "--end-of-options", revision.name+"^{commit}"); err != nil {
		if base != "" {
			revision.problem = firstStderrLine(err)
			if revision.problem == "" {
				revision.problem = "bad revision: " + base
			}
			return revision
		}
		revision.exists = false
	}
	return revision
}

// diffAgainstRevision is computeDiffDocument past the revision check.
func diffAgainstRevision(repoRoot string, revision diffRevision, repoRel string, current []string, currentExists bool) (diffDocument, string) {
	doc := diffDocument{lines: current}
	rev, revisionExists := revision.name, revision.exists
	var old []string
	inBase := false
	if revisionExists {
		out, err := gitcmd.Out(repoRoot, "show", rev+":"+repoRel)
		switch {
		case err != nil:
			// path absent in that revision: a new file
		case bytes.IndexByte(out, 0) >= 0:
			return diffDocument{}, "old version is binary"
		default:
			old, inBase = splitBufferLines(string(out)), true
		}
	}
	switch {
	case !currentExists && !inBase:
		return diffDocument{}, "not in the working tree or " + rev
	case !currentExists:
		doc.deletedFile = true
		doc.rows, doc.adds, doc.dels = buildDiffRows(old, nil)
	case !inBase:
		doc.newFile = true
		doc.rows = make([]diffRow, len(current))
		for i := range current {
			doc.rows[i] = diffRow{kind: diffAdd, bufLine: i}
		}
		doc.adds = len(current)
	default:
		doc.rows, doc.adds, doc.dels = buildDiffRows(old, current)
	}
	return doc, ""
}

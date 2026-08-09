package fuzzy

import (
	"strings"
	"testing"
)

func TestFilterEmptyQueryKeepsOrder(t *testing.T) {
	cands := Prepare([]string{"b.go", "a.go", "c.go"})
	got := Filter("", cands)
	if len(got) != 3 {
		t.Fatalf("want 3 matches, got %d", len(got))
	}
	for i, m := range got {
		if m.Index != i {
			t.Fatalf("order changed: match %d has index %d", i, m.Index)
		}
	}
}

func TestFilterSubsequence(t *testing.T) {
	cands := Prepare([]string{
		"internal/app/render.go",
		"internal/store/store.go",
		"README.md",
	})
	got := Filter("store", cands)
	if len(got) != 1 || got[0].Index != 1 {
		t.Fatalf("want only store.go, got %+v", got)
	}
	if pos := Positions("store", cands[got[0].Index]); len(pos) != 5 {
		t.Fatalf("want 5 matched positions, got %v", pos)
	}
}

func TestFilterRanksBasenameAndBoundaries(t *testing.T) {
	cands := Prepare([]string{
		"internal/app/keys_tree.go", // "tree" in basename after boundary
		"src/subtree/util.go",       // "tree" mid-word in a directory
	})
	got := Filter("tree", cands)
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d", len(got))
	}
	if got[0].Index != 0 {
		t.Fatalf("boundary/basename match should rank first, got %+v", got)
	}
}

func TestFilterCaseInsensitiveAndUTF8(t *testing.T) {
	cands := Prepare([]string{"docs/Résumé.md"})
	if got := Filter("résumé", cands); len(got) != 1 {
		t.Fatalf("utf8 case-insensitive match failed: %+v", got)
	}
	if got := Filter("RESUME", cands); len(got) != 0 {
		// é != e — no transliteration; just document the behavior.
		t.Fatalf("unexpected transliteration match: %+v", got)
	}
}

func TestFilterNoMatch(t *testing.T) {
	if got := Filter("zzz", Prepare([]string{"a.go"})); len(got) != 0 {
		t.Fatalf("want no matches, got %+v", got)
	}
}

// isSubsequence must reject candidates that contain the query runes out of
// order, so the pre-filter agrees with the scorer's full-subsequence result.
func TestPrefilterRejectsOutOfOrder(t *testing.T) {
	// "zx" is not a subsequence of "xz" (x precedes z), so no match.
	if got := Filter("zx", Prepare([]string{"xz.go"})); len(got) != 0 {
		t.Fatalf("out-of-order query must not match: %+v", got)
	}
	// but "xz" is a subsequence of "xaz".
	if got := Filter("xz", Prepare([]string{"xaz.go"})); len(got) != 1 {
		t.Fatalf("in-order subsequence must match: %+v", got)
	}
}

func TestPrepareDirBaseStart(t *testing.T) {
	p := Prepare([]string{"src/app/", "src/app/main.go", "top/"})
	if p[0].baseStart != 4 {
		t.Fatalf("dir basename should anchor on the last segment: %d", p[0].baseStart)
	}
	if p[1].baseStart != 8 {
		t.Fatalf("file basename unchanged: %d", p[1].baseStart)
	}
	if p[2].baseStart != 0 {
		t.Fatalf("top-level dir basename: %d", p[2].baseStart)
	}
}

// A directory-prefix query is a browse, not a search: children of the typed
// directory list first and alphabetically, ahead of scattered subsequence
// matches from elsewhere (all children score identically, so without this the
// order degraded to path length).
func TestFilterDirPrefixListsAlphabetically(t *testing.T) {
	corpus := []string{
		"internal/apphelper/z.go", // scattered match: contains internal/app/ as a subsequence only
		"internal/app/zz.go",
		"internal/app/a_very_long_name.go",
		"internal/app/m.go",
		"internal/app/sub/x.go",
		"deep/internal/app/other.go", // not under the literal prefix
	}
	matches := Filter("internal/app/", Prepare(corpus))
	var got []string
	for _, m := range matches {
		got = append(got, corpus[m.Index])
	}
	want := []string{
		"internal/app/a_very_long_name.go",
		"internal/app/m.go",
		"internal/app/sub/x.go",
		"internal/app/zz.go",
	}
	if len(got) < len(want) {
		t.Fatalf("matches = %v", got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Fatalf("row %d = %q, want %q (full: %v)", i, got[i], w, got)
		}
	}
	for _, rest := range got[len(want):] {
		if strings.HasPrefix(rest, "internal/app/") {
			t.Fatalf("class member %q sorted after non-class matches: %v", rest, got)
		}
	}
}

func TestFilterDirPrefixWithTailKeepsClassFirst(t *testing.T) {
	corpus := []string{
		"internal/apphelper/renderer.go", // non-class scattered match
		"internal/app/render.go",
		"internal/app/render_overlay.go",
	}
	matches := Filter("internal/app/ren", Prepare(corpus))
	if len(matches) < 2 {
		t.Fatalf("matches = %d", len(matches))
	}
	// Class members first; within the class, score order (render.go, the
	// shorter/tighter match, ahead of render_overlay.go).
	if corpus[matches[0].Index] != "internal/app/render.go" {
		t.Fatalf("first = %q", corpus[matches[0].Index])
	}
	if corpus[matches[1].Index] != "internal/app/render_overlay.go" {
		t.Fatalf("second = %q", corpus[matches[1].Index])
	}
}

func TestFilterDirPrefixCaseInsensitive(t *testing.T) {
	corpus := []string{"other/x.go", "Internal/App/b.go", "Internal/App/a.go"}
	matches := Filter("internal/app/", Prepare(corpus))
	if len(matches) != 2 {
		t.Fatalf("matches = %d", len(matches))
	}
	if corpus[matches[0].Index] != "Internal/App/a.go" || corpus[matches[1].Index] != "Internal/App/b.go" {
		t.Fatalf("order = %q, %q", corpus[matches[0].Index], corpus[matches[1].Index])
	}
}

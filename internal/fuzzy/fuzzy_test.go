package fuzzy

import "testing"

func TestFilterEmptyQueryKeepsOrder(t *testing.T) {
	cands := []string{"b.go", "a.go", "c.go"}
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
	cands := []string{
		"internal/app/render.go",
		"internal/store/store.go",
		"README.md",
	}
	got := Filter("store", cands)
	if len(got) != 1 || got[0].Index != 1 {
		t.Fatalf("want only store.go, got %+v", got)
	}
	if len(got[0].Positions) != 5 {
		t.Fatalf("want 5 matched positions, got %v", got[0].Positions)
	}
}

func TestFilterRanksBasenameAndBoundaries(t *testing.T) {
	cands := []string{
		"internal/app/keys_tree.go", // "tree" in basename after boundary
		"src/subtree/util.go",       // "tree" mid-word in a directory
	}
	got := Filter("tree", cands)
	if len(got) != 2 {
		t.Fatalf("want 2 matches, got %d", len(got))
	}
	if got[0].Index != 0 {
		t.Fatalf("boundary/basename match should rank first, got %+v", got)
	}
}

func TestFilterCaseInsensitiveAndUTF8(t *testing.T) {
	cands := []string{"docs/Résumé.md"}
	if got := Filter("résumé", cands); len(got) != 1 {
		t.Fatalf("utf8 case-insensitive match failed: %+v", got)
	}
	if got := Filter("RESUME", cands); len(got) != 0 {
		// é != e — no transliteration; just document the behavior.
		t.Fatalf("unexpected transliteration match: %+v", got)
	}
}

func TestFilterNoMatch(t *testing.T) {
	if got := Filter("zzz", []string{"a.go"}); len(got) != 0 {
		t.Fatalf("want no matches, got %+v", got)
	}
}

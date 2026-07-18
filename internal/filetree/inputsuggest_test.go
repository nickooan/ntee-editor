package filetree

import "testing"

func TestBuildInputSuggestionsOrderAndDedup(t *testing.T) {
	visible := []FileTreeEntry{
		{Name: "app", RelativePath: "app", CommandValue: "app/", Type: "directory"},
		{Name: "app.go", RelativePath: "app.go", CommandValue: "app.go", Type: "file"},
	}
	all := []string{"app.go", "internal/app/deep.go"}

	got := BuildInputSuggestions(visible, all, "app", MaxInputSuggestions)
	if len(got) < 3 {
		t.Fatalf("want ≥3 suggestions, got %+v", got)
	}
	// Prefix matches over the visible tree come before fuzzy corpus hits.
	if got[0].InsertText != "app/" || got[1].InsertText != "app.go" {
		t.Fatalf("prefix order wrong: %+v", got[:2])
	}
	// app.go appears in both visible and corpus — deduped.
	count := 0
	for _, s := range got {
		if s.InsertText == "app.go" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("dedup failed: %+v", got)
	}
}

func TestBuildInputSuggestionsFuzzyFindsCollapsed(t *testing.T) {
	// Nothing visible matches, but the corpus (collapsed dirs) does.
	got := BuildInputSuggestions(nil, []string{"internal/store/store.go"}, "stg", 8)
	if len(got) != 1 || got[0].Entry.RelativePath != "internal/store/store.go" {
		t.Fatalf("fuzzy corpus hit missing: %+v", got)
	}
	if got[0].Entry.Name != "store.go" {
		t.Fatalf("corpus entry name: %+v", got[0].Entry)
	}
}

func TestBuildInputSuggestionsSkipsColonAndEmpty(t *testing.T) {
	if got := BuildInputSuggestions(nil, []string{"a.go"}, ":w", 8); got != nil {
		t.Fatalf("colon command should not suggest: %+v", got)
	}
	if got := BuildInputSuggestions(nil, []string{"a.go"}, "  ", 8); got != nil {
		t.Fatalf("blank should not suggest: %+v", got)
	}
}

func TestBuildInputSuggestionsCap(t *testing.T) {
	all := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		all = append(all, "dir/file"+string(rune('a'+i))+".go")
	}
	if got := BuildInputSuggestions(nil, all, "file", 8); len(got) != 8 {
		t.Fatalf("cap failed: %d", len(got))
	}
}

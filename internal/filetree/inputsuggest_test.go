package filetree

import "testing"

func TestBuildInputSuggestionsOrderAndDedup(t *testing.T) {
	visible := []FileTreeEntry{
		{Name: "app", RelativePath: "app", CommandValue: "app/", Type: "directory"},
		{Name: "app.go", RelativePath: "app.go", CommandValue: "app.go", Type: "file"},
	}
	all := []string{"app.go", "internal/app/deep.go"}

	got := BuildInputSuggestions(visible, all, nil, "app", MaxInputSuggestions)
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
	got := BuildInputSuggestions(nil, []string{"internal/store/store.go"}, nil, "stg", 8)
	if len(got) != 1 || got[0].Entry.RelativePath != "internal/store/store.go" {
		t.Fatalf("fuzzy corpus hit missing: %+v", got)
	}
	if got[0].Entry.Name != "store.go" {
		t.Fatalf("corpus entry name: %+v", got[0].Entry)
	}
}

func TestBuildInputSuggestionsSkipsColonAndEmpty(t *testing.T) {
	if got := BuildInputSuggestions(nil, []string{"a.go"}, nil, ":w", 8); got != nil {
		t.Fatalf("colon command should not suggest: %+v", got)
	}
	if got := BuildInputSuggestions(nil, []string{"a.go"}, nil, "  ", 8); got != nil {
		t.Fatalf("blank should not suggest: %+v", got)
	}
}

func TestBuildInputSuggestionsCap(t *testing.T) {
	all := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		all = append(all, "dir/file"+string(rune('a'+i))+".go")
	}
	if got := BuildInputSuggestions(nil, all, nil, "file", 8); len(got) != 8 {
		t.Fatalf("cap failed: %d", len(got))
	}
}

func TestDirsFromMtimes(t *testing.T) {
	got := DirsFromMtimes(map[string]int64{"": 1, "app": 2, "app/sub": 3, "lib": 4})
	want := []string{"app/", "app/sub/", "lib/"}
	if len(got) != len(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
	if DirsFromMtimes(nil) != nil {
		t.Fatal("empty signature should yield nil")
	}
}

func TestBuildInputSuggestionsDirsFuzzyAndDedup(t *testing.T) {
	// The tree shows app/ expanded-visible; app/sub/ is collapsed and only
	// reachable through the dir corpus.
	visible := []FileTreeEntry{
		{Name: "app", RelativePath: "app", CommandValue: "app/", Type: "directory"},
	}
	files := []string{"app/main.go"}
	dirs := []string{"app/", "app/sub/"}

	got := BuildInputSuggestions(visible, files, dirs, "app/", 8)

	appCount := 0
	foundSub := false
	for _, s := range got {
		if s.InsertText == "app/" {
			appCount++
		}
		if s.InsertText == "app/sub/" {
			foundSub = true
			if s.Source != "directory" || s.Entry.Type != "directory" {
				t.Fatalf("dir suggestion type wrong: %+v", s)
			}
			if s.Entry.Name != "sub" || s.Entry.RelativePath != "app/sub" || s.Entry.CommandValue != "app/sub/" {
				t.Fatalf("dir suggestion entry wrong: %+v", s.Entry)
			}
		}
	}
	if appCount != 1 {
		t.Fatalf("visible dir should dedupe the corpus dir: %+v", got)
	}
	if !foundSub {
		t.Fatalf("collapsed subdir missing from suggestions: %+v", got)
	}
}

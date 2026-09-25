package filetree

import "testing"

func entriesFixture() []FileTreeEntry {
	return []FileTreeEntry{
		{Name: "app", RelativePath: "app", CommandValue: "app/", Type: "directory"},
		{Name: "render.go", RelativePath: "app/render.go", CommandValue: "app/render.go", Type: "file"},
		{Name: "readme.md", RelativePath: "readme.md", CommandValue: "readme.md", Type: "file"},
	}
}

func TestBuildExpandedDirectoryPaths(t *testing.T) {
	got := BuildExpandedDirectoryPaths("a/b")
	if !got["a"] || got["a/b"] {
		t.Fatalf("a/b should expand only a: %v", got)
	}
	got = BuildExpandedDirectoryPaths("a/b/")
	if !got["a"] || !got["a/b"] {
		t.Fatalf("a/b/ should expand a and a/b: %v", got)
	}
	if len(BuildExpandedDirectoryPaths("")) != 0 {
		t.Fatal("empty command should expand nothing")
	}
}

func TestFindFileTreeMatchIndexRanking(t *testing.T) {
	entries := entriesFixture()
	if got := FindFileTreeMatchIndex(entries, "app/render.go"); got != 1 {
		t.Fatalf("exact: got %d", got)
	}
	if got := FindFileTreeMatchIndex(entries, "app"); got != 0 {
		t.Fatalf("prefix over substring: got %d", got)
	}
	if got := FindFileTreeMatchIndex(entries, "adme"); got != 2 {
		t.Fatalf("substring: got %d", got)
	}
	if got := FindFileTreeMatchIndex(entries, ""); got != -1 {
		t.Fatalf("empty input: got %d", got)
	}
}

func TestFindFileTreeMatchIndexFullPathBeatsName(t *testing.T) {
	// A nested file listed first shares its name with a root-level file: the
	// root file's exact full path must win over the earlier name match.
	entries := []FileTreeEntry{
		{Name: "web", RelativePath: "web", CommandValue: "web/", Type: "directory"},
		{Name: "main.go", RelativePath: "web/main.go", CommandValue: "web/main.go", Type: "file"},
		{Name: "main.go", RelativePath: "main.go", CommandValue: "main.go", Type: "file"},
	}
	if got := FindFileTreeMatchIndex(entries, "main.go"); got != 2 {
		t.Fatalf("full path should beat an earlier name match: got %d", got)
	}
	// With no full-path match, the first exact name still wins.
	if got := FindFileTreeMatchIndex(entries[:2], "main.go"); got != 1 {
		t.Fatalf("exact name: got %d", got)
	}
}

func TestResolveHighlightedEntryAncestorFallback(t *testing.T) {
	entries := entriesFixture()
	// No entry matches "app/zzz.go", but the expanded ancestor app/ does.
	if got := ResolveHighlightedEntry(entries, "app/zzz.go"); got != 0 {
		t.Fatalf("ancestor fallback: got %d", got)
	}
}

func TestResolveParentDirectoryCommand(t *testing.T) {
	if p, ok := ResolveParentDirectoryCommand("a/b/c"); !ok || p != "a/b/" {
		t.Fatalf("got %q ok=%v", p, ok)
	}
	if p, ok := ResolveParentDirectoryCommand("a"); !ok || p != "" {
		t.Fatalf("top-level parent should be root: %q ok=%v", p, ok)
	}
	if _, ok := ResolveParentDirectoryCommand(""); ok {
		t.Fatal("empty command has no parent")
	}
}

func TestResolveSidebarCommand(t *testing.T) {
	if got := ResolveSidebarCommand("typed", "sel"); got != "typed" {
		t.Fatalf("typed wins: %q", got)
	}
	if got := ResolveSidebarCommand("", "sel"); got != "sel" {
		t.Fatalf("empty falls back: %q", got)
	}
	if got := ResolveSidebarCommand(":w", "sel"); got != "sel" {
		t.Fatalf("colon command falls back: %q", got)
	}
}

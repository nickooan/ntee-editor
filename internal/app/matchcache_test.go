package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatchCacheMemoizes(t *testing.T) {
	c := &matchCache{}
	content, query := "foo bar\nfoo baz\n", "foo"

	first := c.get(content, query)
	if len(first) != 2 {
		t.Fatalf("matches = %d, want 2", len(first))
	}
	second := c.get(content, query)
	if len(first) != 0 && &first[0] != &second[0] {
		t.Fatal("repeat call should return the cached slice, not recompute")
	}

	// Either key changing recomputes.
	if got := c.get(content, "baz"); len(got) != 1 {
		t.Fatalf("query change: matches = %d, want 1", len(got))
	}
	if got := c.get("foo\n", "foo"); len(got) != 1 {
		t.Fatalf("content change: matches = %d, want 1", len(got))
	}

	// An empty query is cached like any other key (no stale carry-over).
	if got := c.get(content, ""); len(got) != 0 {
		t.Fatalf("empty query: matches = %d, want 0", len(got))
	}
}

func TestMatchCacheNilReceiverFallsBack(t *testing.T) {
	var c *matchCache
	if got := c.get("foo\n", "foo"); len(got) != 1 {
		t.Fatalf("nil cache should still compute: %d matches", len(got))
	}
}

func TestTreeEntriesMemoizedPerMessage(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644))

	first := m.treeEntries()
	if len(first) == 0 {
		t.Fatal("fixture tree should have entries")
	}
	second := m.treeEntries()
	if &first[0] != &second[0] {
		t.Fatal("same message: second call should return the memoized slice")
	}

	// A new message (seq bump, as Update does) recomputes.
	m.frames.seq++
	third := m.treeEntries()
	if len(third) != len(first) {
		t.Fatalf("recompute changed results: %d vs %d", len(third), len(first))
	}
	if &first[0] == &third[0] {
		t.Fatal("new message: entries should be rebuilt")
	}

	// Explicit invalidation forces a rebuild within the same message.
	before := m.treeEntries()
	m.invalidateTreeEntries()
	after := m.treeEntries()
	if &before[0] == &after[0] {
		t.Fatal("invalidateTreeEntries should force a rebuild")
	}

	// A nil frames pointer (zero-value Model in tests) still works.
	m.frames = nil
	if got := m.treeEntries(); len(got) != len(first) {
		t.Fatalf("nil frames fallback: %d entries, want %d", len(got), len(first))
	}
}

func TestRegexCacheMemoizes(t *testing.T) {
	c := &regexCache{}
	first := c.multiline("foo")
	if first == nil || !first.MatchString("a foo b") {
		t.Fatalf("compiled regex broken: %v", first)
	}
	if second := c.multiline("foo"); second != first {
		t.Fatal("same query should return the cached regex")
	}
	if third := c.multiline("bar"); third == first || !third.MatchString("bar") {
		t.Fatalf("query change should recompile: %v", third)
	}
	var nilCache *regexCache
	if re := nilCache.multiline("foo"); re == nil || !re.MatchString("foo") {
		t.Fatal("nil cache should still compile")
	}
}

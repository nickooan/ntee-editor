package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

func TestTreeEntriesMemoizedUntilTheirInputsChange(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("x"), 0o644))

	first := m.treeEntries()
	if len(first) == 0 {
		t.Fatal("fixture tree should have entries")
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	m = next.(Model)
	m.selectedCommand = ""
	m.command = ""
	if second := m.treeEntries(); &first[0] != &second[0] {
		t.Fatal("a message that changes no tree input must not rebuild the tree")
	}

	// Explicit invalidation (root, corpus, git, focus, :touch…) rebuilds.
	m.invalidateTreeEntries()
	rebuilt := m.treeEntries()
	if &first[0] == &rebuilt[0] || len(rebuilt) != len(first) {
		t.Fatal("invalidateTreeEntries should force an identical rebuild")
	}

	// So does age: changes nothing reports still show up.
	m.frames.treeBuiltAt = time.Now().Add(-treeEntriesMaxAge)
	if aged := m.treeEntries(); &aged[0] == &rebuilt[0] {
		t.Fatal("an old memo is rebuilt")
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

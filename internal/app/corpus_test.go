package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/store"
)

// corpusModel builds a model over a temp project containing a normal file, a
// gitignored file, and a .git directory — so tests can assert what the search
// corpus includes and excludes.
func corpusModel(t *testing.T) (Model, string) {
	t.Helper()
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.log\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "keep.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "debug.log"), []byte("noise\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644))
	m := New(config.Default(), store.NewMemory(), root, "", nil)
	m.width, m.height, m.ready = 100, 30, true
	return m, root
}

func containsStr(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestCorpusExcludesGitAndGitignored is the exclusion invariant: the cached
// corpus stores the already-filtered BuildAllEntries output, so .git and
// gitignored files must never appear in it.
func TestCorpusExcludesGitAndGitignored(t *testing.T) {
	m, _ := corpusModel(t)
	m, _ = m.ensureCorpus()

	if !containsStr(m.corpus, "keep.go") {
		t.Fatalf("expected keep.go in corpus, got %v", m.corpus)
	}
	if containsStr(m.corpus, "debug.log") {
		t.Fatalf("gitignored file leaked into corpus: %v", m.corpus)
	}
	for _, rel := range m.corpus {
		if rel == ".git" || strings.HasPrefix(rel, ".git/") {
			t.Fatalf(".git entry leaked into corpus: %q", rel)
		}
	}
}

// TestCorpusMsgSwapsCorpus covers both halves of the freshness design: the
// cache does not reflect an external change on its own (no per-keystroke walk),
// and a background rebuild delivered via corpusMsg swaps in the fresh list and
// clears the rebuilding flag.
func TestCorpusMsgSwapsCorpus(t *testing.T) {
	m, root := corpusModel(t)
	m, _ = m.ensureCorpus()
	m.corpusRebuilding = true // pretend a background rebuild is in flight

	// A new file appears externally; the cached corpus does not see it yet.
	must(t, os.WriteFile(filepath.Join(root, "added.go"), []byte("package main\n"), 0o644))
	if containsStr(m.corpus, "added.go") {
		t.Fatal("cache should not reflect the new file before a rebuild")
	}

	// Simulate the background rebuild landing.
	gi := filetree.LoadGitignore(root)
	fresh := filetree.BuildAllEntries(root, config.Default().Tree.Ignore, gi)
	next, _ := m.Update(corpusMsg{files: fresh, gi: gi, builtAt: time.Now()})
	m = next.(Model)

	if !containsStr(m.corpus, "added.go") {
		t.Fatalf("rebuild did not surface the new file: %v", m.corpus)
	}
	if m.corpusRebuilding {
		t.Fatal("corpusRebuilding should be reset after corpusMsg")
	}
}

// TestCorpusBuiltOncePerSession is the core of the performance fix: typing in
// the query bar must not re-walk the tree on every keystroke. The corpus is
// built once (on the first keystroke, cold cache) and reused thereafter.
func TestCorpusBuiltOncePerSession(t *testing.T) {
	m, _ := newTestModel(t, nil)

	m = runes(m, "m") // first keystroke: cold build
	first := m.corpusBuiltAt
	if first.IsZero() {
		t.Fatal("corpus not built after the first keystroke")
	}

	m = runes(m, "ain.go") // more keystrokes, all within corpusTTL
	if !m.corpusBuiltAt.Equal(first) {
		t.Fatalf("corpus rebuilt during typing: first=%v now=%v", first, m.corpusBuiltAt)
	}
}

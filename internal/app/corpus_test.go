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
	m = rebuildCorpusNow(m)

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
	m = rebuildCorpusNow(m)
	m.corpusRebuilding = true // pretend a background rebuild is in flight

	// A new file appears externally; the cached corpus does not see it yet.
	must(t, os.WriteFile(filepath.Join(root, "added.go"), []byte("package main\n"), 0o644))
	if containsStr(m.corpus, "added.go") {
		t.Fatal("cache should not reflect the new file before a rebuild")
	}

	// Simulate the background rebuild landing.
	gi := filetree.LoadGitignore(root)
	fresh, dirMtimes, truncated := filetree.BuildAllEntries(root, config.Default().Tree.Ignore, gi, config.Default().Tree.MaxIndexFiles)
	next, _ := m.Update(corpusMsg{files: fresh, gi: gi, dirMtimes: dirMtimes, truncated: truncated, builtAt: time.Now()})
	m = next.(Model)

	if !containsStr(m.corpus, "added.go") {
		t.Fatalf("rebuild did not surface the new file: %v", m.corpus)
	}
	if m.corpusRebuilding {
		t.Fatal("corpusRebuilding should be reset after corpusMsg")
	}
}

// TestCorpusBuiltOncePerSession is the core of the performance fix: typing in
// the query bar must never walk the tree on the UI goroutine, and the corpus,
// once built, is reused across keystrokes (no rebuild within corpusTTL).
func TestCorpusBuiltOncePerSession(t *testing.T) {
	m, _ := newTestModel(t, nil) // warm (rebuildCorpusNow in the fixture)
	first := m.corpusBuiltAt
	if first.IsZero() {
		t.Fatal("fixture should start warm")
	}

	m = runes(m, "main.go") // keystrokes within corpusTTL
	if !m.corpusBuiltAt.Equal(first) {
		t.Fatalf("corpus rebuilt during typing: first=%v now=%v", first, m.corpusBuiltAt)
	}
}

// TestColdCorpusNeverBuildsOnKeystroke: on a cold cache the keystroke path
// must not walk synchronously — New marks the Init build in flight, so
// ensureCorpus just waits for the corpusMsg.
func TestColdCorpusNeverBuildsOnKeystroke(t *testing.T) {
	m, _ := corpusModel(t)
	if !m.corpusRebuilding {
		t.Fatal("cold New should mark the Init build in flight")
	}
	m, cmd := m.ensureCorpus()
	if cmd != nil {
		t.Fatal("ensureCorpus must not fire a second build while one is in flight")
	}
	if !m.corpusBuiltAt.IsZero() || len(m.corpus) != 0 {
		t.Fatal("ensureCorpus must not build synchronously on a cold cache")
	}
}

// TestSignatureValidDetectsChange guards against the root-mtime-only bug: the
// stat-sweep must invalidate when anything in the tree changes.
func TestSignatureValidDetectsChange(t *testing.T) {
	_, root := corpusModel(t)
	gi := filetree.LoadGitignore(root)
	files, dirMtimes, _ := filetree.BuildAllEntries(root, config.Default().Tree.Ignore, gi, 50000)
	idx := store.CorpusIndex{Version: store.CorpusVersion, Files: files, DirMtimes: dirMtimes}

	if !signatureValidFor(root, idx) {
		t.Fatal("a freshly built signature should be valid")
	}
	// Adding a file bumps its containing directory's mtime.
	must(t, os.WriteFile(filepath.Join(root, "newfile.go"), []byte("x\n"), 0o644))
	if signatureValidFor(root, idx) {
		t.Fatal("signature should be invalid after an external add")
	}
	// A missing directory and an empty signature are both invalid.
	if signatureValidFor(root, store.CorpusIndex{Version: store.CorpusVersion, DirMtimes: map[string]int64{"ghost": 1}}) {
		t.Fatal("removed dir should invalidate")
	}
	if signatureValidFor(root, store.CorpusIndex{Version: store.CorpusVersion}) {
		t.Fatal("empty signature should be invalid")
	}
}

// TestWarmStartLoadsPersistedCorpus: after a rebuild persists to the DB, a fresh
// model over the same DB+root reuses the corpus and skips the walk in Init.
func TestWarmStartLoadsPersistedCorpus(t *testing.T) {
	db := store.NewMemory()
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "keep.go"), []byte("package main\n"), 0o644))

	m := New(config.Default(), db, root, "", nil)
	gi := filetree.LoadGitignore(root)
	files, dirMtimes, _ := filetree.BuildAllEntries(root, config.Default().Tree.Ignore, gi, 50000)
	next, _ := m.Update(corpusMsg{files: files, gi: gi, dirMtimes: dirMtimes, builtAt: time.Now()})
	_ = next.(Model) // corpusMsg handler persisted the index to db

	m2 := New(config.Default(), db, root, "", nil)
	if m2.corpusBuiltAt.IsZero() {
		t.Fatal("expected a warm start (corpus loaded from db)")
	}
	if !containsStr(m2.corpus, "keep.go") {
		t.Fatalf("warm-started corpus missing keep.go: %v", m2.corpus)
	}
	if m2.splash {
		t.Fatal("warm start must not show the splash")
	}

	// The warm start adopts the index optimistically; the signature check runs
	// in the background and stays silent while the tree is unchanged…
	if m2.pendingValidate == nil {
		t.Fatal("warm start should queue a background signature check")
	}
	if msg := m2.validateCorpusCmd(*m2.pendingValidate)(); msg != nil {
		t.Fatalf("valid signature must not trigger a rebuild, got %T", msg)
	}
	// …and delivers a fresh corpus when the tree changed under the cache.
	must(t, os.WriteFile(filepath.Join(root, "added.go"), []byte("x\n"), 0o644))
	msg, ok := m2.validateCorpusCmd(*m2.pendingValidate)().(corpusMsg)
	if !ok {
		t.Fatal("changed signature should run the rebuild")
	}
	if !containsStr(msg.files, "added.go") {
		t.Fatalf("background rebuild missing the new file: %v", msg.files)
	}
}

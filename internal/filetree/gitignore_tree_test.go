package filetree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildFileTreeEntriesGitignore(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\n")
	write("secret.log", "x\n")
	write("ignoredDir/inside.txt", "y\n")

	gi := CompileGitignore([]string{"*.log", "ignoredDir/"})
	expanded := map[string]bool{"ignoredDir": true}
	entries := BuildFileTreeEntries(root, expanded, nil, gi, nil)

	byPath := map[string]FileTreeEntry{}
	for _, e := range entries {
		byPath[e.RelativePath] = e
	}

	check := func(rel string, want bool) {
		e, ok := byPath[rel]
		if !ok {
			t.Fatalf("entry %q missing from tree", rel)
		}
		if e.Dimmed != want {
			t.Errorf("%q Dimmed = %v, want %v", rel, e.Dimmed, want)
		}
	}
	check("main.go", false)
	check("secret.log", true)
	check("ignoredDir", true)
	check("ignoredDir/inside.txt", true) // inherits from the ignored dir
}

func TestBuildAllEntriesExcludesGitignored(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go")
	write("keep.txt")
	write("dist/bundle.js")
	write("node_modules/pkg/index.js")

	gi := CompileGitignore([]string{"dist/", "node_modules/"})
	set := map[string]bool{}
	files, _, _ := BuildAllEntries(root, nil, gi, 0)
	for _, f := range files {
		set[f] = true
	}
	if set["dist/bundle.js"] || set["node_modules/pkg/index.js"] {
		t.Errorf("gitignored subtrees must be excluded from the corpus: %v", set)
	}
	if !set["main.go"] || !set["keep.txt"] {
		t.Errorf("tracked files must remain: %v", set)
	}
}

func TestBuildFileTreeEntriesNilGitignore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range BuildFileTreeEntries(root, nil, nil, nil, nil) {
		if e.Dimmed {
			t.Fatalf("nil matcher must not flag %q", e.RelativePath)
		}
	}
}

// writeTree is a test helper: writes each rel→content pair, creating parents.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func dimByPath(t *testing.T, entries []FileTreeEntry) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, e := range entries {
		out[e.RelativePath] = e.Dimmed
	}
	return out
}

func corpusSet(files []string) map[string]bool {
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}
	return set
}

// A .gitignore inside a subdirectory dims its matches in the tree and keeps
// them out of the search corpus, without affecting siblings or the root.
func TestNestedGitignore(t *testing.T) {
	ClearDirCache()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app.log":          "x", // root-level, no rule applies to it
		"sub/.gitignore":   "*.log\n",
		"sub/app.log":      "x",
		"sub/app.go":       "x",
		"sub/deep/x.log":   "x", // *.log applies at any depth below sub
		"sub/deep/keep.go": "x",
	})

	expanded := map[string]bool{"sub": true, "sub/deep": true}
	dim := dimByPath(t, BuildFileTreeEntries(root, expanded, nil, nil, nil))

	if !dim["sub/app.log"] {
		t.Error("sub/app.log must be dimmed by sub/.gitignore")
	}
	if !dim["sub/deep/x.log"] {
		t.Error("sub/deep/x.log must be dimmed by sub/.gitignore at depth")
	}
	if dim["sub/app.go"] {
		t.Error("sub/app.go must not be dimmed")
	}
	if dim["app.log"] {
		t.Error("root app.log must not be dimmed (nested rule is scoped to sub/)")
	}

	corpus := corpusSet(mustFiles(BuildAllEntries(root, nil, nil, 0)))
	if corpus["sub/app.log"] || corpus["sub/deep/x.log"] {
		t.Errorf("nested-gitignored files must be excluded from the corpus: %v", corpus)
	}
	if !corpus["sub/app.go"] || !corpus["app.log"] {
		t.Errorf("non-ignored files must remain in the corpus: %v", corpus)
	}
}

// A deeper .gitignore overrides a shallower one, including a `!` re-include of a
// file the root ignores.
func TestNestedGitignoreOverridesRoot(t *testing.T) {
	ClearDirCache()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":     "*.log\n",
		"x.log":          "x",
		"sub/.gitignore": "!keep.log\n",
		"sub/keep.log":   "x", // re-included by the nested negation
		"sub/other.log":  "x", // still ignored by the root rule
	})

	gi := LoadGitignore(root)
	expanded := map[string]bool{"sub": true}
	dim := dimByPath(t, BuildFileTreeEntries(root, expanded, nil, gi, nil))

	if dim["sub/keep.log"] {
		t.Error("sub/keep.log must be re-included (not dimmed) by nested !keep.log")
	}
	if !dim["sub/other.log"] {
		t.Error("sub/other.log must stay dimmed by the root *.log")
	}
	if !dim["x.log"] {
		t.Error("root x.log must stay dimmed")
	}

	corpus := corpusSet(mustFiles(BuildAllEntries(root, nil, gi, 0)))
	if !corpus["sub/keep.log"] {
		t.Errorf("re-included sub/keep.log must be in the corpus: %v", corpus)
	}
	if corpus["sub/other.log"] || corpus["x.log"] {
		t.Errorf("root-ignored *.log files must stay out of the corpus: %v", corpus)
	}
}

// Locks in issue #2: an entry matched by the ROOT .gitignore grays even when
// deeply nested (unanchored patterns match at any depth).
func TestRootGitignoreDimsAtDepth(t *testing.T) {
	ClearDirCache()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		".gitignore":   "*.log\n",
		"a/b/deep.log": "x",
		"a/b/keep.go":  "x",
	})

	gi := LoadGitignore(root)
	expanded := map[string]bool{"a": true, "a/b": true}
	dim := dimByPath(t, BuildFileTreeEntries(root, expanded, nil, gi, nil))

	if !dim["a/b/deep.log"] {
		t.Error("a/b/deep.log must be dimmed by root *.log at depth")
	}
	if dim["a/b/keep.go"] {
		t.Error("a/b/keep.go must not be dimmed")
	}
}

// mustFiles unwraps BuildAllEntries' (files, dirMtimes, truncated) for tests
// that only care about the file list.
func mustFiles(files []string, _ map[string]int64, _ bool) []string {
	return files
}

// node_modules is soft-ignored: shown in the tree (dimmed) but kept out of the
// search corpus, even without a .gitignore.
func TestNodeModulesDimmedButNotSearched(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go")
	write("node_modules/pkg/index.js")

	// Tree: node_modules appears and is dimmed; expanding it keeps children dimmed.
	expanded := map[string]bool{"node_modules": true, "node_modules/pkg": true}
	byPath := map[string]FileTreeEntry{}
	for _, e := range BuildFileTreeEntries(root, expanded, nil, nil, nil) {
		byPath[e.RelativePath] = e
	}
	nm, ok := byPath["node_modules"]
	if !ok {
		t.Fatal("node_modules must appear in the tree")
	}
	if !nm.Dimmed {
		t.Error("node_modules must be dimmed")
	}
	if child, ok := byPath["node_modules/pkg/index.js"]; !ok || !child.Dimmed {
		t.Errorf("node_modules children must appear and be dimmed: %+v", child)
	}
	if main, ok := byPath["main.go"]; !ok || main.Dimmed {
		t.Errorf("main.go must appear undimmed: %+v", main)
	}

	// Corpus: node_modules stays out of search.
	files, _, _ := BuildAllEntries(root, nil, nil, 0)
	for _, f := range files {
		if strings.HasPrefix(f, "node_modules/") {
			t.Fatalf("node_modules must be excluded from the corpus: %q", f)
		}
	}
}

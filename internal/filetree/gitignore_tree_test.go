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
	entries := BuildFileTreeEntries(root, expanded, nil, gi)

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
	for _, e := range BuildFileTreeEntries(root, nil, nil, nil) {
		if e.Dimmed {
			t.Fatalf("nil matcher must not flag %q", e.RelativePath)
		}
	}
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
	for _, e := range BuildFileTreeEntries(root, expanded, nil, nil) {
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

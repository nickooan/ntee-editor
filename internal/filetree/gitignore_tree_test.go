package filetree

import (
	"os"
	"path/filepath"
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
		if e.Gitignored != want {
			t.Errorf("%q Gitignored = %v, want %v", rel, e.Gitignored, want)
		}
	}
	check("main.go", false)
	check("secret.log", true)
	check("ignoredDir", true)
	check("ignoredDir/inside.txt", true) // inherits from the ignored dir
}

func TestBuildFileTreeEntriesNilGitignore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.log"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, e := range BuildFileTreeEntries(root, nil, nil, nil) {
		if e.Gitignored {
			t.Fatalf("nil matcher must not flag %q", e.RelativePath)
		}
	}
}

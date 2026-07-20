package filetree

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkfile(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// node_modules and .git are always skipped, regardless of config/gitignore.
func TestBuildAllEntriesSkipsAlwaysIgnore(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "main.go")
	mkfile(t, root, "node_modules/pkg/index.js")
	mkfile(t, root, ".git/config")

	files, _, _ := BuildAllEntries(root, nil, nil, 0)
	set := map[string]bool{}
	for _, f := range files {
		set[f] = true
	}
	if !set["main.go"] {
		t.Fatalf("main.go missing: %v", files)
	}
	for f := range set {
		if strings.HasPrefix(f, "node_modules/") || strings.HasPrefix(f, ".git/") {
			t.Fatalf("alwaysIgnore leaked into corpus: %q", f)
		}
	}
}

func TestBuildAllEntriesHonorsMaxFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 20; i++ {
		mkfile(t, root, fmt.Sprintf("f%02d.txt", i))
	}
	files, _, truncated := BuildAllEntries(root, nil, nil, 5)
	if !truncated {
		t.Fatal("expected truncated=true when the cap is hit")
	}
	if len(files) != 5 {
		t.Fatalf("expected exactly the cap (5), got %d", len(files))
	}

	// maxFiles<=0 means unlimited.
	all, _, trunc := BuildAllEntries(root, nil, nil, 0)
	if trunc || len(all) != 20 {
		t.Fatalf("unlimited walk wrong: truncated=%v n=%d", trunc, len(all))
	}
}

func TestFindRepoRoot(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "repoA/.git/config")
	mkfile(t, root, "repoA/src/x.go")
	mkfile(t, root, "plain/y.go")

	repoA := filepath.Join(root, "repoA")
	// A file inside a repo resolves to that repo.
	if got := FindRepoRoot(root, filepath.Join(root, "repoA/src/x.go")); got != repoA {
		t.Fatalf("file in repoA: got %q want %q", got, repoA)
	}
	// A file with no .git up to the editor root resolves to the editor root.
	if got := FindRepoRoot(root, filepath.Join(root, "plain/y.go")); got != root {
		t.Fatalf("file in plain: got %q want %q", got, root)
	}
	// When the editor root itself is the repo, every file resolves to it.
	mkfile(t, root, ".git/config")
	if got := FindRepoRoot(root, filepath.Join(root, "plain/y.go")); got != root {
		t.Fatalf("root-is-repo: got %q want %q", got, root)
	}
	// A path outside the editor root falls back to the editor root.
	if got := FindRepoRoot(repoA, filepath.Join(root, "plain/y.go")); got != repoA {
		t.Fatalf("outside editor root: got %q want %q", got, repoA)
	}
}

// In a monorepo, LSP rooting must land on the file's sub-project, not the outer
// .git — otherwise the language server loads the whole repo.
func TestFindProjectRoot(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, ".git/config")                                 // monorepo root
	mkfile(t, root, "frontends/app/package.json")                  // sub-project
	mkfile(t, root, "frontends/app/tsconfig.json")                 // sub-project
	mkfile(t, root, "frontends/app/src/pages/x/component.vue")     // deep file
	mkfile(t, root, "loose/notes.txt")                             // no marker above it

	sub := filepath.Join(root, "frontends", "app")
	if got := FindProjectRoot(root, filepath.Join(root, "frontends/app/src/pages/x/component.vue")); got != sub {
		t.Fatalf("deep file should resolve to the sub-project: got %q want %q", got, sub)
	}
	// A file whose only marker up the tree is the repo .git resolves to the root.
	if got := FindProjectRoot(root, filepath.Join(root, "loose/notes.txt")); got != root {
		t.Fatalf("file with no nearer marker should resolve to the repo root: got %q want %q", got, root)
	}
}

func TestBuildAllEntriesReturnsDirSignature(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "sub/a.go")

	_, dirMtimes, _ := BuildAllEntries(root, nil, nil, 0)
	if _, ok := dirMtimes[""]; !ok {
		t.Fatalf("root dir ('') missing from signature: %v", dirMtimes)
	}
	if _, ok := dirMtimes["sub"]; !ok {
		t.Fatalf("sub dir missing from signature: %v", dirMtimes)
	}
}

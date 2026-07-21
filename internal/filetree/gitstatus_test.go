package filetree

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func TestParsePorcelain(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want []string
	}{
		{"empty", "", nil},
		{"modified", " M internal/app/app.go\x00", []string{"internal/app/app.go"}},
		{"staged", "M  main.go\x00", []string{"main.go"}},
		{"untracked file", "?? notes.txt\x00", []string{"notes.txt"}},
		{"untracked dir", "?? newpkg/\x00", []string{"newpkg"}},
		{"rename carries origin", "R  new/name.go\x00old/name.go\x00", []string{"new/name.go", "old/name.go"}},
		{"mixed", " M a.go\x00?? b/\x00A  c.md\x00", []string{"a.go", "b", "c.md"}},
	}
	for _, c := range cases {
		got := parsePorcelain([]byte(c.out))
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: parsePorcelain = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMarkDirtyAncestors(t *testing.T) {
	set := map[string]bool{}
	markDirty(set, "a/b/c.go")
	var got []string
	for k := range set {
		got = append(got, k)
	}
	sort.Strings(got)
	want := []string{"a", "a/b", "a/b/c.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("markDirty set = %v, want %v", got, want)
	}
}

func TestBuildFileTreeEntriesUncommitted(t *testing.T) {
	ClearDirCache()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"clean.go":      "x",
		"sub/dirty.go":  "x",
		"sub/clean.go":  "x",
		"other/keep.go": "x",
	})

	dirty := map[string]bool{}
	markDirty(dirty, "sub/dirty.go")

	// sub collapsed: the folded dir itself must carry the flag.
	byPath := map[string]FileTreeEntry{}
	for _, e := range BuildFileTreeEntries(root, nil, nil, nil, dirty) {
		byPath[e.RelativePath] = e
	}
	if !byPath["sub"].Uncommitted {
		t.Error("collapsed dir with a dirty file inside must be flagged")
	}
	if byPath["other"].Uncommitted || byPath["clean.go"].Uncommitted {
		t.Error("clean entries must not be flagged")
	}

	// sub expanded: the dirty file is flagged, its clean sibling is not.
	byPath = map[string]FileTreeEntry{}
	for _, e := range BuildFileTreeEntries(root, map[string]bool{"sub": true}, nil, nil, dirty) {
		byPath[e.RelativePath] = e
	}
	if !byPath["sub/dirty.go"].Uncommitted || !byPath["sub"].Uncommitted {
		t.Error("dirty file and its parent dir must be flagged")
	}
	if byPath["sub/clean.go"].Uncommitted {
		t.Error("clean sibling must not be flagged")
	}

	// nil set: nothing flagged.
	for _, e := range BuildFileTreeEntries(root, nil, nil, nil, nil) {
		if e.Uncommitted {
			t.Fatalf("nil dirty set must flag nothing, got %q", e.RelativePath)
		}
	}
}

// TestGitDirtySetIntegration exercises the real git CLI in a scratch repo.
func TestGitDirtySetIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q")
	write("committed.go", "v1")
	write("sub/tracked.go", "v1")
	git("add", "-A")
	git("commit", "-q", "-m", "init")

	// Clean repo → empty set.
	dirty, ok := GitDirtySet(root)
	if !ok {
		t.Fatal("GitDirtySet must succeed in a repo")
	}
	if len(dirty) != 0 {
		t.Fatalf("clean repo must have an empty set, got %v", dirty)
	}

	// Modify a tracked file + add an untracked one.
	write("sub/tracked.go", "v2")
	write("newdir/fresh.go", "x")
	dirty, ok = GitDirtySet(root)
	if !ok {
		t.Fatal("GitDirtySet must succeed")
	}
	// newdir/fresh.go must be listed individually (--untracked-files=all), not
	// collapsed into a bare "newdir/" record — the Ctrl+U finder needs openable
	// file paths, and the ancestor dirs still mark for the sidebar.
	for _, want := range []string{"sub/tracked.go", "sub", "newdir", "newdir/fresh.go"} {
		if !dirty[want] {
			t.Errorf("dirty set missing %q: %v", want, dirty)
		}
	}
	if dirty["committed.go"] {
		t.Errorf("clean file must not be dirty: %v", dirty)
	}

	// Non-repo → feature off.
	if _, ok := GitDirtySet(t.TempDir()); ok {
		t.Fatal("non-repo must report ok=false")
	}
}

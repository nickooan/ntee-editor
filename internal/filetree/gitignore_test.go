package filetree

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitignoreMatch(t *testing.T) {
	g := CompileGitignore([]string{
		"# a comment",
		"",
		"*.log",
		"!keep.log",
		"/dist",
		"build/",
		"node_modules/**",
		"**/tmp",
		"docs/gen.md",
	})

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"a.log", false, true},               // *.log basename, any depth
		{"src/b.log", false, true},           // *.log deeper
		{"keep.log", false, false},           // re-included by !keep.log
		{"dist", true, true},                 // anchored /dist (dir)
		{"dist", false, true},                // anchored /dist (file)
		{"src/dist", true, false},            // /dist only at root
		{"build", true, true},                // build/ matches a dir
		{"build", false, false},              // build/ does not match a file
		{"node_modules/x/y.js", false, true}, // node_modules/**
		{"tmp", true, true},                  // **/tmp at root
		{"a/b/tmp", false, true},             // **/tmp deeper
		{"docs/gen.md", false, true},         // anchored path
		{"other/gen.md", false, false},       // anchored, not elsewhere
		{"main.go", false, false},            // unmatched
	}
	for _, c := range cases {
		if got := g.Match(c.path, c.isDir); got != c.want {
			t.Errorf("Match(%q, dir=%v) = %v, want %v", c.path, c.isDir, got, c.want)
		}
	}
}

func TestGitignoreMatchState(t *testing.T) {
	g := CompileGitignore([]string{"*.log", "!keep.log"})

	if matched, ignored := g.MatchState("a.log", false); !matched || !ignored {
		t.Errorf("a.log: matched=%v ignored=%v, want true/true", matched, ignored)
	}
	// keep.log is matched (by both rules) but re-included, so ignored is false.
	if matched, ignored := g.MatchState("keep.log", false); !matched || ignored {
		t.Errorf("keep.log: matched=%v ignored=%v, want true/false", matched, ignored)
	}
	// An unmatched path must report matched=false so a chained deeper file wins.
	if matched, _ := g.MatchState("main.go", false); matched {
		t.Error("main.go: matched=true, want false (no rule applied)")
	}

	var nilG *Gitignore
	if matched, _ := nilG.MatchState("x.log", false); matched {
		t.Error("nil matcher must report matched=false")
	}
}

func TestGitignoreNilMatchesNothing(t *testing.T) {
	var g *Gitignore
	if g.Match("anything.log", false) {
		t.Fatal("nil matcher should match nothing")
	}
}

func TestLoadGitignoreMissing(t *testing.T) {
	if g := LoadGitignore(t.TempDir()); g != nil {
		t.Fatal("no .gitignore should yield a nil matcher")
	}
}

func TestLoadGitignoreReads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g := LoadGitignore(root)
	if g == nil || !g.Match("x.tmp", false) {
		t.Fatal("LoadGitignore should read and match *.tmp")
	}
}

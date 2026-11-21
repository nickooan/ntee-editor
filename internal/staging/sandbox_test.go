package staging

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickooan/ntee-editor/internal/store"
)

// sandboxWorkspace is a workspace holding repos repoA (with node_modules),
// repoB and other, and a loose folder outside any repo.
func sandboxWorkspace(t *testing.T) (*Registry, string, string, *store.MemoryAgentEditLog) {
	t.Helper()
	root := t.TempDir()
	for rel, content := range map[string]string{
		"repoA/.git/HEAD":           "ref: refs/heads/main\n",
		"repoA/a.go":                "package a\n",
		"repoA/untouched.go":        "package a // untouched\n",
		"repoA/node_modules/dep.js": "module.exports = 1\n",
		"repoB/.git/HEAD":           "ref: refs/heads/main\n",
		"repoB/b.go":                "package b\n",
		"other/.git/HEAD":           "ref: refs/heads/main\n",
		"other/o.go":                "package o\n",
		"loose/notes.txt":           "notes\n",
	} {
		must(t, os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755))
		must(t, os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644))
	}
	boxes := t.TempDir()
	log := store.NewMemoryAgentEditLog()
	return NewRegistry(root, log).WithSandboxDir(boxes), root, boxes, log
}

// endAll ends every given agent's part, so the session finalises.
func endAll(t *testing.T, registry *Registry, session string, agents ...string) {
	t.Helper()
	for _, agent := range agents {
		_, _, err := registry.MarkAgentEnded(session, agent)
		must(t, err)
	}
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestASandboxHoldsEveryRepoTheSessionTouches(t *testing.T) {
	registry, root, _, _ := sandboxWorkspace(t)
	_, err := registry.Write("S", "agent-a", "repoA/a.go", "package a\n\nfunc A() {}\n")
	must(t, err)
	_, err = registry.Write("S", "agent-b", "repoB/b.go", "package b\n\nfunc B() {}\n")
	must(t, err)
	if _, err := registry.Sandbox("S", false, nil); err == nil || !strings.Contains(err.Error(), "still being edited (by agent-a, agent-b)") {
		t.Fatalf("no sandbox while agents edit: %v", err)
	}
	endAll(t, registry, "S", "agent-a")
	if _, err := registry.Sandbox("S", false, nil); err == nil || !strings.Contains(err.Error(), "(by agent-b)") {
		t.Fatalf("nor while one still does: %v", err)
	}
	endAll(t, registry, "S", "agent-b")

	result, err := registry.Sandbox("S", false, nil)
	must(t, err)
	if strings.Join(result.Units, ",") != "repoA,repoB" || result.Placed != 2 || result.BuiltAt.IsZero() {
		t.Fatalf("one box for both agents' repos: %+v", result)
	}
	box := result.Path
	if readFile(t, box, "repoA/a.go") != "package a\n\nfunc A() {}\n" || readFile(t, box, "repoB/b.go") != "package b\n\nfunc B() {}\n" {
		t.Fatal("the staged contents are in place")
	}
	if readFile(t, box, "repoA/untouched.go") != "package a // untouched\n" || readFile(t, box, "repoA/node_modules/dep.js") != "module.exports = 1\n" {
		t.Fatal("the rest of each repo is there, ignored folders included")
	}
	if exists(filepath.Join(box, "other")) || exists(filepath.Join(box, "loose")) {
		t.Fatal("untouched repos are not copied")
	}
	if readFile(t, root, "repoA/a.go") != "package a\n" {
		t.Fatal("the workspace is untouched")
	}

	// agent-a fixes something: the session goes back to editing and the box
	// built from the earlier edits goes.
	fix, err := registry.Replace("S", "agent-a", "repoA/a.go", Replacement{Old: "func A() {}", New: "func A() int { return 1 }"})
	must(t, err)
	if !fix.Reopened || exists(box) {
		t.Fatalf("the fix reopens the session and drops the box: %+v", fix)
	}
	_, err = registry.Write("S", "agent-b", "loose/notes.txt", "more notes\n")
	must(t, err)
	endAll(t, registry, "S", "agent-a", "agent-b")
	result, err = registry.Sandbox("S", false, []string{"other"})
	must(t, err)
	if strings.Join(result.Units, ",") != "loose,other,repoA,repoB" || result.Placed != 3 {
		t.Fatalf("the next box holds every repo touched, and include: %+v", result)
	}
	if !strings.Contains(readFile(t, box, "repoA/a.go"), "return 1") || readFile(t, box, "loose/notes.txt") != "more notes\n" || readFile(t, box, "other/o.go") != "package o\n" {
		t.Fatal("the box has the newer contents")
	}
	if _, err := registry.Sandbox("S", false, []string{"../outside"}); err == nil {
		t.Fatal("include stays inside the project")
	}
}

func TestAFreshSandboxPicksUpTheDisk(t *testing.T) {
	registry, root, _, _ := sandboxWorkspace(t)
	_, err := registry.Write("S", "", "repoA/a.go", "staged\n")
	must(t, err)
	endAll(t, registry, "S", "")
	result, err := registry.Sandbox("S", false, nil)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(root, "repoA/untouched.go"), []byte("changed on disk\n"), 0o644))
	if _, err := registry.Sandbox("S", false, nil); err != nil || readFile(t, result.Path, "repoA/untouched.go") != "package a // untouched\n" {
		t.Fatalf("a refresh keeps the copy (%v)", err)
	}
	if _, err := registry.Sandbox("S", true, nil); err != nil || readFile(t, result.Path, "repoA/untouched.go") != "changed on disk\n" || readFile(t, result.Path, "repoA/a.go") != "staged\n" {
		t.Fatalf("fresh rebuilds it from the disk, staged files on top (%v)", err)
	}
}

func TestSandboxesGoWhenTheSessionFinishes(t *testing.T) {
	registry, _, boxes, log := sandboxWorkspace(t)
	for _, session := range []string{"applied", "discarded", "dropped", "kept"} {
		_, err := registry.Write(session, "", "repoA/a.go", "x\n")
		must(t, err)
		endAll(t, registry, session, "")
		_, err = registry.Sandbox(session, false, nil)
		must(t, err)
	}
	record, _ := registry.Snapshot("applied")
	_, err := registry.Finish("applied", store.AgentEditPolicyReview, registry.ApplyFiles("applied", record.Files, nil), 20)
	must(t, err)
	_, err = registry.Discard("discarded", "the user", 20)
	must(t, err)
	registry.DropSandbox("dropped")
	for _, gone := range []string{"applied", "discarded", "dropped"} {
		if exists(filepath.Join(boxes, gone)) {
			t.Fatalf("the sandbox of %s is gone", gone)
		}
	}
	if !exists(filepath.Join(boxes, "kept")) {
		t.Fatal("a pending session keeps its sandbox")
	}
	if _, err := registry.Sandbox("discarded", false, nil); err == nil {
		t.Fatal("a finished session gets no sandbox")
	}

	// The next start sweeps what no pending session owns.
	must(t, os.MkdirAll(filepath.Join(boxes, "ghost", "repoA"), 0o755))
	must(t, os.MkdirAll(filepath.Join(boxes, sandboxTrashPrefix+"old"), 0o755))
	restarted := NewRegistry(registry.workspaceRoot, log).WithSandboxDir(boxes)
	must(t, restarted.Load())
	if exists(filepath.Join(boxes, "ghost")) || !exists(filepath.Join(boxes, "kept")) {
		t.Fatal("Load sweeps an orphaned sandbox and keeps a pending one's")
	}
	deadline := time.Now().Add(2 * time.Second)
	for exists(filepath.Join(boxes, sandboxTrashPrefix+"old")) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if exists(filepath.Join(boxes, sandboxTrashPrefix+"old")) {
		t.Fatal("and empties the trash")
	}
}

func TestTheWorkspaceRepoIsOneUnit(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644))
	registry := NewRegistry(root, store.NewMemoryAgentEditLog()).WithSandboxDir(t.TempDir())
	_, err := registry.Write("S", "", "main.go", "package main\n\nfunc main() {}\n")
	must(t, err)
	endAll(t, registry, "S", "")
	result, err := registry.Sandbox("S", false, nil)
	must(t, err)
	if strings.Join(result.Units, ",") != "." || readFile(t, result.Path, "go.mod") != "module x\n" || readFile(t, result.Path, "main.go") != "package main\n\nfunc main() {}\n" {
		t.Fatalf("the box is the whole repo: %+v", result)
	}
	if RepoName(".") != "the project" {
		t.Fatal(RepoName("."))
	}
}

func TestSandboxRefusals(t *testing.T) {
	registry, _, _, _ := sandboxWorkspace(t)
	if _, err := registry.Sandbox("nothing", false, nil); err == nil {
		t.Fatal("no such session")
	}
	plain := NewRegistry(t.TempDir(), store.NewMemoryAgentEditLog())
	if _, err := plain.Sandbox("S", false, nil); err == nil || !strings.Contains(err.Error(), "keeps no sandboxes") {
		t.Fatalf("no sandbox folder: %v", err)
	}
}

func TestTheFallbackCopySkipsGitAndStopsAtItsBudget(t *testing.T) {
	_, root, _, _ := sandboxWorkspace(t)
	target := filepath.Join(t.TempDir(), "copy")
	budget := int64(1 << 20)
	must(t, copyTree(filepath.Join(root, "repoA"), target, &budget))
	if exists(filepath.Join(target, ".git")) || readFile(t, target, "node_modules/dep.js") != "module.exports = 1\n" {
		t.Fatal("a plain copy skips .git and keeps the rest")
	}
	small := int64(5)
	if err := copyTree(filepath.Join(root, "repoA"), filepath.Join(t.TempDir(), "copy"), &small); !errors.Is(err, errTooLargeToCopy) {
		t.Fatalf("past its budget the copy stops: %v", err)
	}
}

func TestConcurrentSandboxCallsBuildOneBox(t *testing.T) {
	registry, _, _, _ := sandboxWorkspace(t)
	_, err := registry.Write("S", "", "repoA/a.go", "x\n")
	must(t, err)
	endAll(t, registry, "S", "")
	var wait sync.WaitGroup
	results := make([]SandboxResult, 6)
	for index := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := registry.Sandbox("S", false, nil)
			must(t, err)
			results[index] = result
		}()
	}
	wait.Wait()
	for _, result := range results {
		if !result.BuiltAt.Equal(results[0].BuiltAt) || strings.Join(result.Units, ",") != "repoA" {
			t.Fatalf("one build: %+v vs %+v", result, results[0])
		}
	}
}

func TestSessionsInRepoNamesTheOpenSessionsToJoin(t *testing.T) {
	registry, _, _, _ := sandboxWorkspace(t)
	_, err := registry.Write("S", "agent-a", "repoA/a.go", "x\n")
	must(t, err)
	_, err = registry.Write("T", "agent-b", "repoB/b.go", "y\n")
	must(t, err)
	repo, sessions := registry.SessionsInRepo("repoA/untouched.go")
	if repo != "repoA" || len(sessions) != 1 || sessions[0].Session != "S" || strings.Join(sessions[0].Editing, ",") != "agent-a" {
		t.Fatalf("S is changing repoA: %s %+v", repo, sessions)
	}
	if _, sessions := registry.SessionsInRepo("repoA/a.go", "S"); len(sessions) != 0 {
		t.Fatal("the reader's own session is left out")
	}
	_, _, err = registry.MarkAgentEnded("S", "agent-a")
	must(t, err)
	if _, sessions := registry.SessionsInRepo("repoA/a.go"); len(sessions) != 0 {
		t.Fatal("a session every agent ended takes no more edits")
	}
}

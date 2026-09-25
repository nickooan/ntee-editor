package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/lsp"
	"github.com/nickooan/ntee-editor/internal/store"
)

// workspaceTreeFixture writes two nested repos (with .git markers so the
// nested-repo scan finds them) next to newTestModel's root-level main.go.
func workspaceTreeFixture(t *testing.T, root string) {
	t.Helper()
	for _, rel := range []string{"apps/web/.git", "libs/core/.git"} {
		must(t, os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755))
	}
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "main.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "libs", "core", "lib.go"), []byte("package core\n\nvar X = 1\n"), 0o644))
}

// workspaceModel is a model over a two-repo workspace whose nested-repo scan
// has landed (the corpus rebuild runs it).
func workspaceModel(t *testing.T, db store.Backend) (Model, string) {
	t.Helper()
	m, root := newTestModel(t, db)
	workspaceTreeFixture(t, root)
	m = rebuildCorpusNow(m)
	if !slices.Equal(m.workspaceRepos, []string{"apps/web", "libs/core"}) {
		t.Fatalf("nested repos = %v", m.workspaceRepos)
	}
	return m, root
}

// enterRepo picks a repo through Ctrl+W and lands its index (the switch fires
// the rebuild as a cmd, which the key helper does not run).
func enterRepo(t *testing.T, m Model, filter string) Model {
	t.Helper()
	m = key(m, ctrlKey('w'))
	if !m.fuzzyOpen || m.fuzzyPrompt != fuzzyPromptRepo {
		t.Fatalf("picker open=%v prompt=%q", m.fuzzyOpen, m.fuzzyPrompt)
	}
	m = runes(m, filter)
	m = key(m, keyPress(tea.KeyEnter))
	return rebuildCorpusNow(m)
}

// fuzzyTexts lists the finder's current candidates in order.
func fuzzyTexts(m Model) []string {
	var texts []string
	for _, match := range m.fuzzyMatches {
		texts = append(texts, m.fuzzyCorpus[match.Index].Text)
	}
	return texts
}

func treePaths(m Model) []string {
	var paths []string
	for _, entry := range m.treeEntries() {
		paths = append(paths, entry.RelativePath)
	}
	return paths
}

func TestCtrlWRefusesGitRoot(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.workspaceIsRepo = true
	m = key(m, ctrlKey('w'))
	if m.fuzzyOpen {
		t.Fatal("a git root must not open the repo picker")
	}
	if m.errText != "not a workspace directory" {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestCtrlWListsWorkspaceAndRepos(t *testing.T) {
	m, root := workspaceModel(t, nil)
	m = key(m, ctrlKey('w'))
	want := []string{filepath.Base(root) + "/", "apps/web/", "libs/core/"}
	if got := fuzzyTexts(m); !slices.Equal(got, want) {
		t.Fatalf("repo list = %v, want %v", got, want)
	}
	m = runes(m, "core")
	if got := fuzzyTexts(m); !slices.Equal(got, []string{"libs/core/"}) {
		t.Fatalf("filter = %v", got)
	}
}

func TestCtrlWRootsEditorAtRepo(t *testing.T) {
	m, root := workspaceModel(t, nil)
	m = enterRepo(t, m, "core")

	if m.activeRepo != "libs/core" || m.root != filepath.Join(root, "libs", "core") || !m.gitRepo {
		t.Fatalf("root = %q repo = %q gitRepo = %v", m.root, m.activeRepo, m.gitRepo)
	}
	// The tree is the repo: nothing above or beside it.
	paths := treePaths(m)
	if !slices.Contains(paths, "lib.go") || slices.Contains(paths, "main.go") || slices.Contains(paths, "apps") {
		t.Fatalf("tree = %v", paths)
	}
	// The index — which Ctrl+P, the query bar, and Ctrl+G all read — too.
	if !slices.Equal(m.corpus, []string{"lib.go"}) {
		t.Fatalf("corpus = %v", m.corpus)
	}
	m = key(m, ctrlKey('p'))
	if got := fuzzyTexts(m); slices.Contains(got, "main.go") || slices.Contains(got, "apps/web/main.go") || !slices.Contains(got, "lib.go") {
		t.Fatalf("Ctrl+P = %v", got)
	}
	m = key(m, keyPress(tea.KeyEsc))
	m = runes(m, "main")
	for _, suggestion := range m.queryInputSuggestions(m.treeEntries()) {
		if strings.Contains(suggestion.Entry.RelativePath, "main") {
			t.Fatalf("query bar leaked outside the repo: %+v", suggestion)
		}
	}

	// Back to the workspace: the whole tree returns.
	m.command, m.qCursor = "", 0
	m = enterRepo(t, m, filepath.Base(root))
	if m.activeRepo != "" || m.root != root || m.gitRepo {
		t.Fatalf("back at workspace: root = %q repo = %q gitRepo = %v", m.root, m.activeRepo, m.gitRepo)
	}
	if paths := treePaths(m); !slices.Contains(paths, "apps") || !slices.Contains(paths, "main.go") {
		t.Fatalf("workspace tree = %v", paths)
	}
}

func TestCtrlWSameRepoOnlyClosesPicker(t *testing.T) {
	m, _ := workspaceModel(t, nil)
	m = enterRepo(t, m, "core")
	generation := m.rootGen
	m = enterRepo(t, m, "core")
	if m.fuzzyOpen || m.rootGen != generation {
		t.Fatalf("re-picking the current repo must not switch: open=%v gen %d→%d", m.fuzzyOpen, generation, m.rootGen)
	}
}

func TestRootsRememberTheirOwnPosition(t *testing.T) {
	db := store.NewMemory()
	m, root := workspaceModel(t, db)

	m = m.openFileAt("main.go")
	m = enterRepo(t, m, "core")
	if m.openFile != nil || len(m.tabs) != 0 {
		t.Fatalf("a fresh repo starts empty: open=%q tabs=%v", m.openRel, m.tabs)
	}
	m = m.openFileAt("lib.go")

	m = enterRepo(t, m, filepath.Base(root))
	if m.openRel != "main.go" || !slices.Equal(m.tabs, []string{"main.go"}) {
		t.Fatalf("workspace position: open=%q tabs=%v", m.openRel, m.tabs)
	}
	m = enterRepo(t, m, "core")
	if m.openRel != "lib.go" || !slices.Equal(m.tabs, []string{"lib.go"}) {
		t.Fatalf("repo position: open=%q tabs=%v", m.openRel, m.tabs)
	}

	// Relaunch lands back inside the repo, at its file.
	m.saveSession()
	restored := New(config.Default(), db, root, "", nil)
	if restored.activeRepo != "libs/core" || restored.root != filepath.Join(root, "libs", "core") || restored.openRel != "lib.go" {
		t.Fatalf("restored repo=%q root=%q file=%q", restored.activeRepo, restored.root, restored.openRel)
	}
	// And the workspace's position is still its own.
	sess, _ := db.LoadSession()
	if sess.LastFile != "main.go" || sess.Repos["libs/core"].LastFile != "lib.go" {
		t.Fatalf("session = %+v", sess)
	}
}

func TestSwitchRootStashesUnsavedEditsAndSharesHistory(t *testing.T) {
	m, _ := workspaceModel(t, nil)

	// Edit the repo's file from the workspace, then switch without saving.
	m = m.openFileAt("libs/core/lib.go")
	m = runes(m, "// wip")
	m = enterRepo(t, m, "core")

	// Inside the repo the same file comes back with the unsaved edits: the
	// draft is keyed by workspace path, which the repo view maps to "lib.go".
	m = m.openFileAt("lib.go")
	if !m.edit.dirty || !strings.HasPrefix(m.edit.content(), "// wip") {
		t.Fatalf("draft not carried into the repo: dirty=%v content=%q", m.edit.dirty, m.edit.content())
	}
}

func TestStaleRootResultsAreDropped(t *testing.T) {
	m, _ := workspaceModel(t, nil)
	staleCorpus := m.rebuildCorpusCmd()
	staleGeneration := m.rootGen
	m = enterRepo(t, m, "core")

	next, _ := m.Update(staleCorpus())
	m = next.(Model)
	if !slices.Equal(m.corpus, []string{"lib.go"}) {
		t.Fatalf("a late workspace walk replaced the repo index: %v", m.corpus)
	}
	next, _ = m.Update(gitStatusMsg{ok: true, rootGen: staleGeneration, dirty: map[string]bool{"main.go": true}})
	m = next.(Model)
	if m.gitDirty["main.go"] {
		t.Fatal("a late workspace git status must be ignored")
	}
}

func TestRelaunchFallsBackWhenRepoIsGone(t *testing.T) {
	db := store.NewMemory()
	m, root := workspaceModel(t, db)
	m = enterRepo(t, m, "core")
	m.saveSession()
	must(t, os.RemoveAll(filepath.Join(root, "libs", "core", ".git")))

	restored := New(config.Default(), db, root, "", nil)
	if restored.activeRepo != "" || restored.root != root {
		t.Fatalf("vanished repo must fall back: repo=%q root=%q", restored.activeRepo, restored.root)
	}
}

func TestRescanWithoutActiveRepoReturnsToWorkspace(t *testing.T) {
	m, root := workspaceModel(t, nil)
	m = enterRepo(t, m, "core")

	next, _ := m.Update(workspaceReposMsg{repos: []string{"apps/web"}})
	m = next.(Model)
	if m.activeRepo != "" || m.root != root {
		t.Fatalf("repo gone from the scan must switch back: repo=%q root=%q", m.activeRepo, m.root)
	}
	if !strings.Contains(m.notice, "libs/core is gone") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestDefinitionOutsideRepoIsReported(t *testing.T) {
	m, root := workspaceModel(t, nil)
	m = enterRepo(t, m, "core")
	m = m.openFileAt("lib.go")

	outside := lsp.Location{URI: lsp.PathToURI(filepath.Join(root, "apps", "web", "main.go"))}
	next, _ := m.Update(definitionMsg{rel: "lib.go", token: "X", locs: []lsp.Location{outside}})
	m = next.(Model)
	if m.errText != "definition is outside repo libs/core" {
		t.Fatalf("errText = %q", m.errText)
	}
}

// initWorkspaceRepo git-inits dir (committing its current files) so git tests
// have real repos to resolve against.
func initWorkspaceRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=tester"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("commit", "-q", "-m", "init")
}

// workspaceGitFixture is a workspace (rooted at the opened directory) with two
// real nested git repos, the nested-repo scan complete.
func workspaceGitFixture(t *testing.T) (Model, string) {
	t.Helper()
	m, root := newTestModel(t, nil)
	must(t, os.MkdirAll(filepath.Join(root, "apps", "web"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "libs", "core"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "main.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "libs", "core", "lib.go"), []byte("package core\n\nvar X = 1\n"), 0o644))
	initWorkspaceRepo(t, filepath.Join(root, "apps", "web"))
	initWorkspaceRepo(t, filepath.Join(root, "libs", "core"))
	m.workspaceRepos = []string{"apps/web", "libs/core"}
	m.workspaceReposScanned = true
	return m, root
}

func TestWorkspaceGitDiffUsesFileRepo(t *testing.T) {
	m, _ := workspaceGitFixture(t)
	m = m.openFileAt("libs/core/lib.go")
	m.edit.lines[2] = "var X = 2" // unsaved edit against the repo's HEAD

	next, cmd := m.enterDiff("")
	m = next.(Model)
	if cmd == nil || m.mode != modeDiff {
		t.Fatalf("enterDiff: mode=%v cmd=%v err=%q", m.mode, cmd, m.errText)
	}
	res, _ := m.Update(cmd())
	m = res.(Model)
	if m.diffNewFile || m.diffAdds != 1 || m.diffDels != 1 || m.errText != "" {
		t.Fatalf("workspace diff: newFile=%v adds=%d dels=%d err=%q", m.diffNewFile, m.diffAdds, m.diffDels, m.errText)
	}
}

func TestRepoRootGitDiffAndBlame(t *testing.T) {
	m, _ := workspaceGitFixture(t)
	m, _ = m.switchRoot("libs/core")
	m = m.openFileAt("lib.go")
	m.edit.lines[2] = "var X = 2"

	next, cmd := m.enterDiff("")
	m = next.(Model)
	res, _ := m.Update(cmd())
	m = res.(Model)
	if m.diffNewFile || m.diffAdds != 1 || m.diffDels != 1 || m.errText != "" {
		t.Fatalf("repo-root diff: newFile=%v adds=%d dels=%d err=%q", m.diffNewFile, m.diffAdds, m.diffDels, m.errText)
	}

	m = m.exitDiff()
	next, cmd = m.enterBlame()
	m = next.(Model)
	res, _ = m.Update(cmd())
	m = res.(Model)
	if m.blameNewFile || m.errText != "" || !m.blameRows[2].uncommitted || m.blameRows[0].author != "tester" {
		t.Fatalf("repo-root blame: newFile=%v err=%q rows=%+v", m.blameNewFile, m.errText, m.blameRows)
	}
}

func TestWorkspaceGitBlameUsesFileRepo(t *testing.T) {
	m, _ := workspaceGitFixture(t)
	m = m.openFileAt("libs/core/lib.go")
	m.edit.lines[2] = "var X = 2"

	next, cmd := m.enterBlame()
	m = next.(Model)
	if cmd == nil || m.mode != modeBlame {
		t.Fatalf("enterBlame: mode=%v cmd=%v err=%q", m.mode, cmd, m.errText)
	}
	res, _ := m.Update(cmd())
	m = res.(Model)
	if m.blameNewFile || m.errText != "" {
		t.Fatalf("workspace blame: newFile=%v err=%q", m.blameNewFile, m.errText)
	}
	if !m.blameRows[2].uncommitted {
		t.Fatalf("edited line must blame as uncommitted: %+v", m.blameRows[2])
	}
	if row := m.blameRows[0]; row.uncommitted || row.author != "tester" {
		t.Fatalf("committed line = %+v, want tester", row)
	}
}

func TestWorkspaceGitRefusesFileOutsideRepos(t *testing.T) {
	m, _ := workspaceGitFixture(t)
	m = m.openFileAt("main.go") // workspace root, in no nested repo

	next, _ := m.enterDiff("")
	m = next.(Model)
	if m.mode != modeEdit || m.errText != "file is not in a git repository" {
		t.Fatalf("diff: mode=%v err=%q", m.mode, m.errText)
	}
	next, _ = m.enterBlame()
	m = next.(Model)
	if m.mode != modeEdit || m.errText != "file is not in a git repository" {
		t.Fatalf("blame: mode=%v err=%q", m.mode, m.errText)
	}
}

func TestWorkspaceGitLoadingBeforeScan(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.workspaceReposScanned = false
	m = m.openFileAt("main.go")

	next, _ := m.enterDiff("")
	m = next.(Model)
	if m.errText != "git repos still loading" {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestGitScopeDeepestRepoWins(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.workspaceRepos = []string{"libs", "libs/core"}
	m.workspaceReposScanned = true

	root, repoRel, ok, loading := m.gitScopeFor("libs/core/lib.go")
	if !ok || loading {
		t.Fatalf("scope: ok=%v loading=%v", ok, loading)
	}
	if want := filepath.Join(m.root, "libs", "core"); root != want {
		t.Fatalf("root = %q, want %q", root, want)
	}
	if repoRel != "lib.go" {
		t.Fatalf("repoRel = %q, want lib.go", repoRel)
	}
}

func TestRepoRelativePathFallsBack(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.workspaceRepos = []string{"libs/core"}
	m.workspaceReposScanned = true

	if got := m.repoRelativePath("libs/core/lib.go"); got != "lib.go" {
		t.Fatalf("repo-relative = %q, want lib.go", got)
	}
	if got := m.repoRelativePath("main.go"); got != "main.go" {
		t.Fatalf("no-repo fallback = %q, want main.go", got)
	}
}

func TestExecCpfpUsesRepoRelativePath(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.MkdirAll(filepath.Join(root, "libs", "core"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "libs", "core", "lib.go"), []byte("package core\n"), 0o644))
	m.workspaceRepos = []string{"libs/core"}
	m.workspaceReposScanned = true
	m = m.openFileAt("libs/core/lib.go")

	var captured string
	m.copyClipboard = func(s string) error { captured = s; return nil }
	res, _ := m.runExecCommand("cpfp")
	m = res.(Model)
	if captured != "lib.go" {
		t.Fatalf("cpfp = %q, want lib.go", captured)
	}
}

// headerLine is the rendered header row with ANSI escapes stripped.
func headerLine(m Model) string {
	return ansi.Strip(m.renderHeader())
}

func TestHeaderShowsWorkingRepoNextToPath(t *testing.T) {
	m, root := workspaceModel(t, nil)
	if line := headerLine(m); strings.Contains(line, "working repo:") {
		t.Fatalf("workspace header must not name a repo: %q", line)
	}

	m = enterRepo(t, m, "core")
	m.width = 240
	// The label follows the opened directory directly — left-aligned, not
	// pushed to the far edge of a wide terminal.
	want := "ntee-editor " + versionTag() + "  ·  " + root + "  ·  working repo: libs/core"
	if line := headerLine(m); !strings.HasPrefix(line, want) {
		t.Fatalf("header = %q, want prefix %q", line, want)
	}
}

func TestHeaderFitsWidthAndKeepsRepo(t *testing.T) {
	m, _ := workspaceModel(t, nil)
	m = enterRepo(t, m, "core")

	for _, width := range []int{240, 100, 40, 30, 12} {
		m.width = width
		line := headerLine(m)
		if got := len([]rune(line)); got != width {
			t.Fatalf("width %d: header row is %d cells: %q", width, got, line)
		}
	}
	// At a width where the full label fits, it survives the title truncation.
	m.width = 30
	if line := headerLine(m); !strings.Contains(line, "working repo: libs/core") {
		t.Fatalf("repo label lost in a narrow header: %q", line)
	}
}

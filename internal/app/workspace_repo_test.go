package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/config"
	"github.com/nickooan/ntee-editor/internal/filetree"
	"github.com/nickooan/ntee-editor/internal/store"
)

func TestCtrlWRefusesGitRoot(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.gitRepo = true
	m = key(m, ctrlKey('w'))
	if m.fuzzyOpen {
		t.Fatal("a git root must not open the repo picker")
	}
	if m.errText != "not a workspace directory" {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestCtrlWSelectsRepoAndRestoresSession(t *testing.T) {
	db := store.NewMemory()
	m, root := newTestModel(t, db)
	m.workspaceRepos = []string{"apps/web", "libs/core"}
	m.corpus = append(m.corpus, "apps/web/main.go", "libs/core/lib.go")
	m.dirCorpus = append(m.dirCorpus, "apps/", "apps/web/", "libs/", "libs/core/")
	m = m.rebuildQueryScope()

	m = key(m, ctrlKey('w'))
	if !m.fuzzyOpen || m.fuzzyPrompt != "Repo: " {
		t.Fatalf("picker open=%v prompt=%q", m.fuzzyOpen, m.fuzzyPrompt)
	}
	var labels []string
	for _, match := range m.fuzzyMatches {
		labels = append(labels, m.fuzzyCorpus[match.Index].Text)
	}
	wantLabel := filepath.Base(root) + "/"
	if len(labels) != 3 || labels[0] != wantLabel || labels[1] != "apps/web/" || labels[2] != "libs/core/" {
		t.Fatalf("repo list = %v", labels)
	}

	// Typing filters to repo directories only.
	m = runes(m, "core")
	if len(m.fuzzyMatches) != 1 || m.fuzzyCorpus[m.fuzzyMatches[0].Index].Text != "libs/core/" {
		var got []string
		for _, match := range m.fuzzyMatches {
			got = append(got, m.fuzzyCorpus[match.Index].Text)
		}
		t.Fatalf("filter = %v", got)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.fuzzyOpen || m.activeRepo != "libs/core" {
		t.Fatalf("after select open=%v repo=%q", m.fuzzyOpen, m.activeRepo)
	}
	sess, ok := db.LoadSession()
	if !ok || sess.WorkspaceRepo != "libs/core" {
		t.Fatalf("session = %+v ok=%v", sess, ok)
	}

	// Query suggestions stay inside the selected repo. Ctrl+P stays global.
	m = runes(m, "main")
	for _, suggestion := range m.queryInputSuggestions(m.treeEntries()) {
		if suggestion.Entry.RelativePath == "apps/web/main.go" || suggestion.Entry.RelativePath == "main.go" {
			t.Fatalf("query leaked outside libs/core: %+v", suggestion)
		}
	}
	m.command, m.qCursor = "", 0
	m = key(m, ctrlKey('p'))
	if m.fuzzyPrompt != "goto " {
		t.Fatalf("Ctrl+P prompt = %q", m.fuzzyPrompt)
	}
	found := false
	for _, match := range m.fuzzyMatches {
		if m.fuzzyCorpus[match.Index].Text == "apps/web/main.go" {
			found = true
		}
	}
	if !found {
		t.Fatal("Ctrl+P must still list files outside the selected repo")
	}

	// Relaunch restores the repo plus the directory and file that were current.
	must(t, os.MkdirAll(filepath.Join(root, "libs", "core"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "libs", "core", "lib.go"), []byte("package core\n"), 0o644))
	m.selectedCommand = "libs/core/"
	m.openRel = "libs/core/lib.go"
	m.saveSession()
	restored := New(config.Default(), db, root, "", nil)
	if restored.activeRepo != "libs/core" || restored.selectedCommand != "libs/core/" || restored.openRel != "libs/core/lib.go" {
		t.Fatalf("restored repo=%q command=%q file=%q", restored.activeRepo, restored.selectedCommand, restored.openRel)
	}
}

func TestWorkspaceRepoScopesDirtyHighlights(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.workspaceRepos = []string{"apps/web", "libs/core"}
	must(t, os.MkdirAll(filepath.Join(m.root, "apps", "web"), 0o755))
	must(t, os.MkdirAll(filepath.Join(m.root, "libs", "core"), 0o755))
	must(t, os.WriteFile(filepath.Join(m.root, "apps", "web", "main.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(m.root, "libs", "core", "lib.go"), []byte("package core\n"), 0o644))
	// A workspace-wide result marks both repos. A scoped result drops the other.
	// Collapsed directories carry the flag, so the top-level names are enough.
	next, _ := m.Update(gitStatusMsg{
		ok:    true,
		scope: "",
		dirty: map[string]bool{"apps/web/main.go": true, "apps/web": true, "apps": true, "libs/core/lib.go": true, "libs/core": true, "libs": true},
	})
	m = next.(Model)
	if !treeUncommitted(m, "apps") || !treeUncommitted(m, "libs") {
		t.Fatal("workspace scope must highlight every nested repo")
	}

	m.activeRepo = "apps/web"
	next, _ = m.Update(gitStatusMsg{
		ok:    true,
		scope: "apps/web",
		dirty: map[string]bool{"apps/web/main.go": true, "apps/web": true, "apps": true},
	})
	m = next.(Model)
	if !treeUncommitted(m, "apps") {
		t.Fatal("selected repo must stay highlighted")
	}
	if treeUncommitted(m, "libs") {
		t.Fatal("other repos must not stay highlighted")
	}

	// A late workspace-wide scan must not clobber the selected repo.
	next, _ = m.Update(gitStatusMsg{
		ok:    true,
		scope: "",
		dirty: map[string]bool{"libs/core/lib.go": true, "libs/core": true, "libs": true},
	})
	m = next.(Model)
	if treeUncommitted(m, "libs") {
		t.Fatal("stale workspace scan must be ignored while a repo is selected")
	}
}

func treeUncommitted(m Model, rel string) bool {
	for _, entry := range m.treeEntries() {
		if entry.RelativePath == rel && entry.Uncommitted {
			return true
		}
	}
	return false
}

// repoEntries is a small synthetic tree spanning two nested repos.
func repoEntries() []filetree.FileTreeEntry {
	return []filetree.FileTreeEntry{
		{RelativePath: "apps", CommandValue: "apps/", Type: "directory"},
		{RelativePath: "apps/web", CommandValue: "apps/web/", Type: "directory"},
		{RelativePath: "apps/web/main.go", CommandValue: "apps/web/main.go", Type: "file"},
		{RelativePath: "libs", CommandValue: "libs/", Type: "directory"},
		{RelativePath: "libs/core", CommandValue: "libs/core/", Type: "directory"},
		{RelativePath: "libs/core/lib.go", CommandValue: "libs/core/lib.go", Type: "file"},
	}
}

func TestRepoEdgeStopsSidebarNavigation(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.activeRepo = "apps/web"
	m.workspaceRepos = []string{"apps/web", "libs/core"}
	entries := repoEntries()

	// At the repo root, moving up would land on the ancestor "apps" directory:
	// the walk stops and says so.
	m.keyboardSelectedCommand = "apps/web/"
	m = m.moveSidebarSelection(entries, -1)
	if m.keyboardSelectedCommand != "apps/web/" {
		t.Fatalf("walk escaped the repo: %q", m.keyboardSelectedCommand)
	}
	if m.errText != "cannot select outside repo apps/web" {
		t.Fatalf("errText = %q", m.errText)
	}

	// Moving down stays inside the repo.
	m.errText = ""
	m = m.moveSidebarSelection(entries, 1)
	if m.keyboardSelectedCommand != "apps/web/main.go" {
		t.Fatalf("inside walk blocked: %q", m.keyboardSelectedCommand)
	}
}

func TestRepoEdgeSnapsDriftedHighlightToRoot(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.activeRepo = "apps/web"
	entries := repoEntries()

	// A highlight parked outside the repo (mouse click, Ctrl+P open) snaps to
	// the repo root rather than walking out.
	m.keyboardSelectedCommand = "libs/core/lib.go"
	m = m.moveSidebarSelection(entries, 1)
	if m.keyboardSelectedCommand != "apps/web/" {
		t.Fatalf("drifted highlight not snapped: %q", m.keyboardSelectedCommand)
	}
}

func TestRepoEdgeStopsEscAtRoot(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.activeRepo = "apps/web"

	m.selectedCommand = "apps/web/"
	m = m.moveQueryToParentDirectory()
	if m.selectedCommand != "apps/web/" {
		t.Fatalf("Esc escaped the repo: %q", m.selectedCommand)
	}
	if m.errText != "cannot go outside repo apps/web" {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestOutsideRepoFileOpensReadOnly(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.MkdirAll(filepath.Join(root, "apps", "web"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "libs", "core"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "main.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "libs", "core", "lib.go"), []byte("package core\n"), 0o644))
	m.activeRepo = "apps/web"
	m.workspaceRepos = []string{"apps/web", "libs/core"}

	m = m.openFileAt("libs/core/lib.go")
	if m.mode != modeQuery {
		t.Fatalf("outside file must open read-only, mode = %v", m.mode)
	}
	if m.notice != "read-only: outside repo apps/web" {
		t.Fatalf("notice = %q", m.notice)
	}

	// Tab cannot force it into edit mode.
	m = key(m, keyPress(tea.KeyTab))
	if m.mode != modeQuery || m.errText != "read-only: outside repo apps/web" {
		t.Fatalf("Tab entered edit: mode=%v err=%q", m.mode, m.errText)
	}

	// In-file search is still available, but replace is not.
	m = key(m, ctrlKey('f'))
	if m.mode != modeSearch || m.searchPrevMode != modeQuery {
		t.Fatalf("Ctrl+F failed: mode=%v prev=%v", m.mode, m.searchPrevMode)
	}
	m = key(m, ctrlKey('e'))
	if m.mode != modeSearch || m.errText != "read-only view: replace disabled" {
		t.Fatalf("Ctrl+E not refused: mode=%v err=%q", m.mode, m.errText)
	}

	// Ctrl+O with no trail reports rather than doing nothing.
	m = key(m, keyPress(tea.KeyEsc)) // leave search back to the view
	m = key(m, ctrlKey('o'))
	if m.errText != "no jump to return to" {
		t.Fatalf("empty jump trail errText = %q", m.errText)
	}
}

func TestRepoSelectLeavesNowOutsideBuffer(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.MkdirAll(filepath.Join(root, "apps", "web"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "libs", "core"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "main.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "libs", "core", "lib.go"), []byte("package core\n"), 0o644))
	m.workspaceRepos = []string{"apps/web", "libs/core"}

	m = m.openFileAt("apps/web/main.go")
	if m.mode != modeEdit {
		t.Fatalf("inside file should edit, mode = %v", m.mode)
	}

	m, _ = m.selectWorkspaceRepo("libs/core")
	if m.mode != modeQuery {
		t.Fatalf("now-outside buffer should drop to view, mode = %v", m.mode)
	}
	if m.activeRepo != "libs/core" || m.selectedCommand != "libs/core/" {
		t.Fatalf("repo/root not selected: repo=%q command=%q", m.activeRepo, m.selectedCommand)
	}
}

func TestJumpToOutsideRepoReadOnlyAndBack(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.MkdirAll(filepath.Join(root, "apps", "web"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "libs", "core"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "main.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "libs", "core", "lib.go"), []byte("package core\n"), 0o644))
	m.activeRepo = "apps/web"
	m.workspaceRepos = []string{"apps/web", "libs/core"}

	m = m.openFileAt("apps/web/main.go")
	m = m.jumpToLocation("libs/core/lib.go", 0, 0)
	if m.mode != modeQuery || m.openRel != "libs/core/lib.go" {
		t.Fatalf("jump target should be read-only: mode=%v rel=%q", m.mode, m.openRel)
	}
	if len(m.jumpStack) != 1 {
		t.Fatalf("jump trail lost: %d", len(m.jumpStack))
	}

	m = key(m, ctrlKey('o'))
	if m.mode != modeEdit || m.openRel != "apps/web/main.go" {
		t.Fatalf("Ctrl+O failed: mode=%v rel=%q", m.mode, m.openRel)
	}
	if len(m.jumpStack) != 0 {
		t.Fatalf("trail should be empty after return: %d", len(m.jumpStack))
	}
}

func TestWorkspaceRowRemovesRestrictions(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.MkdirAll(filepath.Join(root, "apps", "web"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "libs", "core"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "main.go"), []byte("package main\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "libs", "core", "lib.go"), []byte("package core\n"), 0o644))
	m.workspaceRepos = []string{"apps/web", "libs/core"}
	m.activeRepo = "apps/web"

	m, _ = m.selectWorkspaceRepo("")
	if m.activeRepo != "" {
		t.Fatalf("workspace not selected: %q", m.activeRepo)
	}
	m = m.openFileAt("libs/core/lib.go")
	if m.mode != modeEdit {
		t.Fatalf("whole-workspace file must edit, mode = %v", m.mode)
	}
}

// initWorkspaceRepo git-inits dir (committing its current files) so workspace
// git tests have real repos to resolve against.
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

// workspaceGitFixture sets up two nested repos under the workspace root and
// marks the nested-repo scan complete.
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
	if m.mode != modeEdit {
		t.Fatalf("workspace file should edit, mode = %v", m.mode)
	}
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

func TestHeaderShowsWorkingRepo(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.workspaceRepos = []string{"apps/web", "libs/core"}

	m.activeRepo = "libs/core"
	if line := headerLine(m); !strings.Contains(line, "working repo: libs/core") {
		t.Fatalf("selected repo missing from header: %q", line)
	}

	// The whole-workspace row removes the label.
	m.activeRepo = ""
	if line := headerLine(m); strings.Contains(line, "working repo:") {
		t.Fatalf("workspace header must not name a repo: %q", line)
	}

	// A single-repo opened directory is not a workspace, so it never shows it.
	m.gitRepo = true
	m.activeRepo = "libs/core"
	if line := headerLine(m); strings.Contains(line, "working repo:") {
		t.Fatalf("git-root header must not name a repo: %q", line)
	}
}

func TestHeaderFitsWidthAndKeepsRepo(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.workspaceRepos = []string{"libs/core"}
	m.activeRepo = "libs/core"

	for _, width := range []int{100, 40, 30, 12} {
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

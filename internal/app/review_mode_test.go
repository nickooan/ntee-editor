package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// gitReviewWorkspace builds a workspace of two real nested git repos with a
// committed file each, then changes the working trees: apps/web/main.go is
// modified, apps/web/new.go added, libs/core/lib.go deleted.
func gitReviewWorkspace(t *testing.T) (Model, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m, root := newTestModel(t, nil)
	commit := func(repo string, files map[string]string) {
		t.Helper()
		dir := filepath.Join(root, filepath.FromSlash(repo))
		must(t, os.MkdirAll(dir, 0o755))
		for name, content := range files {
			must(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
		}
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
			cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
		}
	}
	commit("apps/web", map[string]string{"main.go": "package main\n\nfunc main() {}\n"})
	commit("libs/core", map[string]string{"lib.go": "package core\n\nvar X = 1\n"})
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "main.go"), []byte("package main\n\nfunc main() { run() }\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "new.go"), []byte("package main\n\nfunc run() {}\n"), 0o644))
	must(t, os.Remove(filepath.Join(root, "libs", "core", "lib.go")))
	return rebuildCorpusNow(m), root
}

// settleExternal runs cmd and delivers every review/external message it leads to,
// dropping the rest (corpus walks, git polls) so the chain stays finite.
func settleExternal(m Model, cmd tea.Cmd) Model {
	queue := execCmds(cmd)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		switch msg.(type) {
		case reviewPreparedMsg, reviewLoadedMsg, externalStatusSavedMsg, externalHistoryLoadedMsg, externalSettingsMsg:
			var next tea.Cmd
			m, next = deliver(m, msg)
			queue = append(queue, execCmds(next)...)
		}
	}
	return m
}

func reviewOf(t *testing.T, m Model, paths ...string) Model {
	t.Helper()
	m, cmd := m.enterReview(paths, "")
	if m.mode != modeReview {
		t.Fatalf("review did not open: mode=%v err=%q", m.mode, m.errText)
	}
	return settleExternal(m, cmd)
}

func TestReviewAcrossReposShowsModifiedAddedDeleted(t *testing.T) {
	m, _ := gitReviewWorkspace(t)
	m = m.openFileAt("main.go")
	m = reviewOf(t, m, "libs/core/lib.go", "apps/web/new.go", "apps/web/main.go")

	// Sorted by repo, then path, so the left pane groups predictably.
	want := []struct {
		rel    string
		status reviewFileStatus
		repo   string
	}{
		{"apps/web/main.go", reviewModified, "apps/web"},
		{"apps/web/new.go", reviewAdded, "apps/web"},
		{"libs/core/lib.go", reviewDeleted, "libs/core"},
	}
	if len(m.review.files) != len(want) {
		t.Fatalf("files = %d", len(m.review.files))
	}
	for i, w := range want {
		file := m.review.files[i]
		if file.workspaceRel != w.rel || file.status != w.status || file.repoLabel != w.repo {
			t.Fatalf("file %d = %s status=%v repo=%q note=%q", i, file.workspaceRel, file.status, file.repoLabel, file.note)
		}
	}
	for _, row := range m.review.files[1].document.rows {
		if row.kind != diffAdd {
			t.Fatal("a new file must be all green")
		}
	}
	deleted := m.review.files[2].document
	// Lines split like the edit buffer: the trailing newline is a final empty
	// line, exactly as git diff mode counts a new file.
	if deleted.dels != 4 || deleted.adds != 0 {
		t.Fatalf("a deleted file must be all red: +%d -%d", deleted.adds, deleted.dels)
	}
	for _, row := range deleted.rows {
		if row.kind != diffDel {
			t.Fatal("a deleted file must be all red")
		}
	}

	// The cursor lands on the selected file's first change.
	if row := m.review.files[0].document.rows[m.review.position.row]; row.kind == diffCtx {
		t.Fatalf("cursor should start on a change, got row %d", m.review.position.row)
	}

	// The left pane is the folder tree, like the main file tree, with the
	// status badge in the marker column and a space before the name; the
	// main pane names the file and its counts.
	if got := reviewTreeText(m); got != strings.Join([]string{
		"↓ apps",
		"  ↓ web",
		"    M main.go  +1 −1",
		"    A new.go  +4 −0",
		"↓ libs",
		"  ↓ core",
		"    D lib.go  +0 −4",
	}, "\n") {
		t.Fatalf("review tree:\n%s", got)
	}
	list := m.reviewSidebarList()
	if list.selectedIndex != 2 {
		t.Fatalf("the selected file's row is highlighted: %d", list.selectedIndex)
	}
	// The change counts are their own segments, green for additions and red
	// for deletions.
	counts := list.rows[3].segments[2:]
	if len(counts) != 2 || counts[0].text != "  +4" || counts[0].style.GetForeground() != colGreen ||
		counts[1].text != " −0" || counts[1].style.GetForeground() != colRed {
		t.Fatalf("count segments = %+v", counts)
	}
	main := ansi.Strip(m.renderReview(80, 20))
	if !strings.Contains(main, "apps/web/main.go") || !strings.Contains(main, "+1 −1") {
		t.Fatalf("review header:\n%s", main)
	}
}

func TestReviewKeysSwitchFilesJumpChangesAndExit(t *testing.T) {
	m, _ := gitReviewWorkspace(t)
	m = m.openFileAt("main.go")
	m = reviewOf(t, m, "apps/web/main.go", "apps/web/new.go", "libs/core/lib.go")

	m = key(m, shiftKey(tea.KeyDown))
	if m.review.selection.index != 1 {
		t.Fatalf("Shift+↓ selects the next file: %d", m.review.selection.index)
	}
	m = key(m, shiftKey(tea.KeyDown))
	m = key(m, shiftKey(tea.KeyDown)) // clamped at the last file
	if m.review.selection.index != 2 {
		t.Fatalf("selection clamps at the last file: %d", m.review.selection.index)
	}
	m = key(m, shiftKey(tea.KeyUp))
	if m.review.selection.index != 1 {
		t.Fatalf("Shift+↑ selects the previous file: %d", m.review.selection.index)
	}

	// Editing keys are inert: the mode is read-only.
	m = key(m, typeRune('x'))
	if m.mode != modeReview || m.edit.dirty {
		t.Fatal("typing must not reach any buffer")
	}

	m = key(m, keyPress(tea.KeyEscape))
	if m.mode != modeEdit || m.openRel != "main.go" {
		t.Fatalf("Esc returns to the previous file: mode=%v open=%q", m.mode, m.openRel)
	}
}

func TestReviewScopedFinderAndGrep(t *testing.T) {
	m, _ := gitReviewWorkspace(t)
	m = reviewOf(t, m, "apps/web/main.go", "apps/web/new.go", "libs/core/lib.go")

	// Ctrl+P lists only the reviewed files and selects inside review.
	m = key(m, ctrlKey('p'))
	if !m.fuzzyOpen || m.fuzzyPrompt != fuzzyPromptReview || len(m.fuzzyCorpus) != 3 {
		t.Fatalf("finder open=%v prompt=%q candidates=%d", m.fuzzyOpen, m.fuzzyPrompt, len(m.fuzzyCorpus))
	}
	m = runes(m, "lib.go")
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeReview || m.review.selection.index != 2 {
		t.Fatalf("Enter selects the file in review: mode=%v selected=%d", m.mode, m.review.selection.index)
	}

	// Ctrl+G searches the reviewed rows — removed lines included — and lands
	// on the row.
	m = key(m, ctrlKey('g'))
	if !m.grepOpen || m.grepTarget != grepTargetReview || m.grepLoading {
		t.Fatalf("grep open=%v target=%v loading=%v", m.grepOpen, m.grepTarget, m.grepLoading)
	}
	m = runes(m, "var X")
	m, cmd := deliver(m, grepTickMsg{gen: m.grepSearchGen})
	for _, msg := range execCmds(cmd) {
		m, _ = deliver(m, msg)
	}
	if len(m.grepResults) != 1 || m.grepResults[0].rel != "libs/core/lib.go" {
		t.Fatalf("grep results = %+v", m.grepResults)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.mode != modeReview || m.review.selection.index != 2 || m.review.position.row != 2 {
		t.Fatalf("hit lands in review: mode=%v file=%d row=%d", m.mode, m.review.selection.index, m.review.position.row)
	}
}

func TestReviewDropsStaleLoads(t *testing.T) {
	m, _ := gitReviewWorkspace(t)
	m, cmd := m.enterReview([]string{"apps/web/main.go"}, "")
	prepared := execCmds(cmd)
	m = key(m, keyPress(tea.KeyEscape))
	for _, msg := range prepared {
		m, _ = deliver(m, msg)
	}
	if m.mode == modeReview || len(m.review.files) != 0 {
		t.Fatal("a result for a closed review must be dropped")
	}
}

func TestReviewFileOutsideAnyRepoIsAllAdded(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = reviewOf(t, m, "lib/util.ts")
	file := m.review.files[0]
	if file.status != reviewAdded || file.repoRoot != "" || file.note == "" {
		t.Fatalf("no-repo file: status=%v repo=%q note=%q", file.status, file.repoRoot, file.note)
	}
}

// reviewTreeText is the review's left pane as plain text, one row per line.
func reviewTreeText(m Model) string {
	list := m.reviewSidebarList()
	lines := make([]string, len(list.rows))
	for i, row := range list.rows {
		lines[i] = strings.TrimRight(sidebarRowPlain(row), " ")
	}
	return strings.Join(lines, "\n")
}

func TestReviewTreeFoldsOnClickAndUnfoldsOnSelect(t *testing.T) {
	m, _ := gitReviewWorkspace(t)
	m = reviewOf(t, m, "libs/core/lib.go", "apps/web/new.go", "apps/web/main.go")
	if m.review.files[0].workspaceRel != "apps/web/main.go" || m.review.files[2].workspaceRel != "libs/core/lib.go" {
		t.Fatal("files follow the tree's order")
	}

	// Clicking a directory row folds it: its subtree hides and the row counts
	// the reviewed files inside. A second click unfolds it.
	m, _ = m.activateReviewRow(0) // apps
	if got := reviewTreeText(m); !strings.HasPrefix(got, "→ apps  2\n↓ libs") {
		t.Fatalf("folded tree:\n%s", got)
	}
	if list := m.reviewSidebarList(); list.selectedIndex != 0 {
		t.Fatalf("a folded selection highlights its visible folder: %d", list.selectedIndex)
	}
	m, _ = m.activateReviewRow(0)
	if got := reviewTreeText(m); !strings.HasPrefix(got, "↓ apps\n  ↓ web") {
		t.Fatalf("unfolded tree:\n%s", got)
	}

	// Folding libs, then walking onto its file with Shift+↓, reopens it.
	m, _ = m.activateReviewRow(4) // libs
	m = key(m, shiftKey(tea.KeyDown))
	m = key(m, shiftKey(tea.KeyDown))
	if m.review.selection.index != 2 || m.review.collapsed["libs"] {
		t.Fatalf("selection=%d collapsed=%v", m.review.selection.index, m.review.collapsed)
	}
	if list := m.reviewSidebarList(); list.selectedIndex != 6 {
		t.Fatalf("the file inside the reopened folder is highlighted: %d", list.selectedIndex)
	}

	// A click on a file row selects it.
	m, _ = m.activateReviewRow(3) // new.go
	if m.review.selection.index != 1 {
		t.Fatalf("clicked file selected: %d", m.review.selection.index)
	}
}

func TestReviewExpandsTabsForDisplayOnly(t *testing.T) {
	m, root := gitReviewWorkspace(t)
	// A tab-indented line that is unchanged must still diff as context: the
	// expansion runs after the diff.
	target := filepath.Join(root, "apps", "web", "main.go")
	must(t, os.WriteFile(target, []byte("package main\n\nfunc main() { run() }\n\tvar tabbed = 1\n"), 0o644))
	m = reviewOf(t, m, "apps/web/main.go")
	document := m.review.files[0].document
	for i, row := range document.rows {
		if text := document.rowText(i); strings.Contains(text, "\t") {
			t.Fatalf("row %d still has a raw tab: %q", i, text)
		}
		for _, segment := range document.rowHighlights(i) {
			if strings.Contains(segment.Text, "\t") {
				t.Fatalf("row %d highlight keeps a raw tab", i)
			}
		}
		_ = row
	}
	if !strings.Contains(m.review.files[0].corpus, "    var tabbed = 1") {
		t.Fatalf("search corpus matches what is shown: %q", m.review.files[0].corpus)
	}
}

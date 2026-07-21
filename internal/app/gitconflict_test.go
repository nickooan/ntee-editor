package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFindConflictBlocksSimple(t *testing.T) {
	lines := []string{
		"before",
		"<<<<<<< HEAD",
		"ours line",
		"=======",
		"theirs a",
		"theirs b",
		">>>>>>> feature/login",
		"after",
	}
	blocks := findConflictBlocks(lines)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.start != 1 || b.mid != 3 || b.end != 6 || b.base != -1 {
		t.Fatalf("indices = %+v", b)
	}
	if b.oursLabel != "HEAD" || b.theirsLabel != "feature/login" {
		t.Fatalf("labels = %q / %q", b.oursLabel, b.theirsLabel)
	}
	// ours content is lines[start+1:mid]; theirs is lines[mid+1:end].
	if got := strings.Join(lines[b.start+1:b.mid], "\n"); got != "ours line" {
		t.Fatalf("ours = %q", got)
	}
	if got := strings.Join(lines[b.mid+1:b.end], "\n"); got != "theirs a\ntheirs b" {
		t.Fatalf("theirs = %q", got)
	}
}

func TestFindConflictBlocksDiff3(t *testing.T) {
	lines := []string{
		"<<<<<<< HEAD",
		"ours",
		"||||||| merged common ancestors",
		"base",
		"=======",
		"theirs",
		">>>>>>> other",
	}
	blocks := findConflictBlocks(lines)
	if len(blocks) != 1 {
		t.Fatalf("want 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.base != 2 || b.mid != 4 || b.end != 6 {
		t.Fatalf("diff3 indices = %+v", b)
	}
	// Keeping ours must stop before the base section.
	out := resolveConflicts(lines, []conflictBlock{b}, []bool{true})
	if strings.Join(out, "\n") != "ours" {
		t.Fatalf("diff3 ours resolve = %q", out)
	}
}

func TestFindConflictBlocksMalformed(t *testing.T) {
	cases := map[string][]string{
		"unclosed start":       {"<<<<<<< HEAD", "ours", "======="},
		"close before sep":     {"<<<<<<< HEAD", "ours", ">>>>>>> x"},
		"restart before close": {"<<<<<<< HEAD", "ours", "<<<<<<< AGAIN", "o2", "=======", "t2", ">>>>>>> y"},
		"lone separator":       {"code", "=======", "more"},
	}
	for name, lines := range cases {
		blocks := findConflictBlocks(lines)
		if name == "restart before close" {
			// The first start is abandoned; the well-formed inner block survives.
			if len(blocks) != 1 || blocks[0].oursLabel != "AGAIN" {
				t.Fatalf("%s: want the AGAIN block, got %+v", name, blocks)
			}
			continue
		}
		if len(blocks) != 0 {
			t.Fatalf("%s: want no blocks, got %+v", name, blocks)
		}
	}
}

func TestResolveConflictsMultiple(t *testing.T) {
	lines := []string{
		"top",
		"<<<<<<< HEAD",
		"ours1",
		"=======",
		"theirs1",
		">>>>>>> b1",
		"middle",
		"<<<<<<< HEAD",
		"ours2",
		"=======",
		"theirs2",
		">>>>>>> b2",
		"bottom",
	}
	blocks := findConflictBlocks(lines)
	if len(blocks) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(blocks))
	}
	// Keep ours in the first, theirs in the second.
	out := resolveConflicts(lines, blocks, []bool{true, false})
	want := "top\nours1\nmiddle\ntheirs2\nbottom"
	if got := strings.Join(out, "\n"); got != want {
		t.Fatalf("resolve = %q, want %q", got, want)
	}
	// Input slice must be untouched.
	if lines[1] != "<<<<<<< HEAD" {
		t.Fatal("resolveConflicts mutated its input")
	}
}

func TestMatchConflictSide(t *testing.T) {
	b := conflictBlock{oursLabel: "HEAD", theirsLabel: "feature/login"}
	if ko, ok := matchConflictSide(b, "head"); !ok || !ko {
		t.Errorf("head: ko=%v ok=%v, want true/true", ko, ok)
	}
	if ko, ok := matchConflictSide(b, "Feature/Login"); !ok || ko {
		t.Errorf("branch: ko=%v ok=%v, want false/true", ko, ok)
	}
	if _, ok := matchConflictSide(b, "nope"); ok {
		t.Error("unmatched target must report ok=false")
	}
}

// --- integration through the @exec bar ---

// conflictFixture opens a file containing one conflict block and returns the
// model in edit mode with the cursor on the given line.
func conflictFixture(t *testing.T, cursorLine int) Model {
	t.Helper()
	m, root := newTestModel(t, nil)
	content := "package main\n" +
		"\n" +
		"<<<<<<< HEAD\n" +
		"const databaseUrl = \"prod\"\n" +
		"=======\n" +
		"const databaseUrl = \"dev\"\n" +
		">>>>>>> feature/login\n" +
		"\n" +
		"func main() {}\n"
	must(t, os.WriteFile(filepath.Join(root, "conf.go"), []byte(content), 0o644))
	m = m.openFileAt("conf.go")
	m.edit.cy, m.edit.cx = cursorLine, 0
	return m
}

func TestExecGitScfKeepsOurs(t *testing.T) {
	m := conflictFixture(t, 2) // cursor on the <<<<<<< line, no selection
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "git scf head")
	m = ctrl(m, tea.KeyEnter)

	got := m.edit.content()
	if !strings.Contains(got, `const databaseUrl = "prod"`) {
		t.Fatalf("ours content missing: %q", got)
	}
	if strings.Contains(got, `"dev"`) || strings.Contains(got, "<<<<<<<") ||
		strings.Contains(got, "=======") || strings.Contains(got, ">>>>>>>") {
		t.Fatalf("markers/theirs not removed: %q", got)
	}
	if m.mode != modeEdit {
		t.Fatalf("should return to edit mode, got %v", m.mode)
	}
	if !m.edit.dirty {
		t.Fatal("buffer should be dirty after resolve")
	}
	if !strings.HasPrefix(m.notice, "resolved 1 conflict") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestExecGitScfKeepsTheirsBySelection(t *testing.T) {
	m := conflictFixture(t, 2)
	// Line-wise select the whole block: Ctrl+A twice, then extend down to the
	// closing marker (lines 2..6).
	m = ctrl(m, tea.KeyCtrlA)
	m = ctrl(m, tea.KeyCtrlA)
	for i := 0; i < 4; i++ {
		m = ctrl(m, tea.KeyShiftDown)
	}
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "git scf feature/login")
	m = ctrl(m, tea.KeyEnter)

	got := m.edit.content()
	if !strings.Contains(got, `"dev"`) || strings.Contains(got, `"prod"`) {
		t.Fatalf("theirs not kept: %q", got)
	}
	if strings.Contains(got, "<<<<<<<") {
		t.Fatalf("markers remain: %q", got)
	}
}

func TestExecGitScfBadLabelStaysInExec(t *testing.T) {
	m := conflictFixture(t, 2)
	before := m.edit.content()
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "git scf nope")
	m = ctrl(m, tea.KeyEnter)

	if m.mode != modeExec {
		t.Fatalf("bad label should stay in exec, got %v", m.mode)
	}
	if !strings.Contains(m.errText, "HEAD") || !strings.Contains(m.errText, "feature/login") {
		t.Fatalf("errText should name both labels: %q", m.errText)
	}
	if m.edit.content() != before {
		t.Fatal("buffer must be untouched on a bad label")
	}
}

func TestExecGitScfPartialSelectionErrors(t *testing.T) {
	m := conflictFixture(t, 2)
	before := m.edit.content()
	// Select from the <<<<<<< line down to only the ======= line (2..4): the
	// closing >>>>>>> at line 6 is left out, so the block is partially selected.
	m = ctrl(m, tea.KeyCtrlA)
	m = ctrl(m, tea.KeyCtrlA)
	m = ctrl(m, tea.KeyShiftDown)
	m = ctrl(m, tea.KeyShiftDown)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "git scf head")
	m = ctrl(m, tea.KeyEnter)

	if m.mode != modeExec {
		t.Fatalf("partial selection should stay in exec, got %v", m.mode)
	}
	if !strings.Contains(m.errText, "not fully selected") {
		t.Fatalf("errText should report a partial selection: %q", m.errText)
	}
	if m.edit.content() != before {
		t.Fatal("buffer must be untouched on a partial selection")
	}
}

func TestExecGitScfNoConflictInRegion(t *testing.T) {
	m := conflictFixture(t, 0) // cursor on "package main", away from the block
	before := m.edit.content()
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "git scf head")
	m = ctrl(m, tea.KeyEnter)

	if m.mode != modeExec {
		t.Fatal("should stay in exec when no block is in the region")
	}
	if m.errText != "no conflict block in selection" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.edit.content() != before {
		t.Fatal("buffer must be untouched")
	}
}

func TestExecGitScfUndoRestoresConflict(t *testing.T) {
	m := conflictFixture(t, 2)
	before := m.edit.content()
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "git scf head")
	m = ctrl(m, tea.KeyEnter)
	if m.edit.content() == before {
		t.Fatal("resolve did not change the buffer")
	}
	m = ctrl(m, tea.KeyCtrlZ)
	if m.edit.content() != before {
		t.Fatalf("undo should restore the conflict, got %q", m.edit.content())
	}
}

func TestExecGitUnknownSubcommand(t *testing.T) {
	m := conflictFixture(t, 2)
	m = ctrl(m, tea.KeyCtrlE)
	m = runes(m, "git foo")
	m = ctrl(m, tea.KeyEnter)
	if m.mode != modeExec {
		t.Fatal("unknown git subcommand should stay in exec")
	}
	if !strings.Contains(m.errText, "unknown git command") {
		t.Fatalf("errText = %q", m.errText)
	}
}

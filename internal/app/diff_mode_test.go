package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// rowsFixture builds display rows for a 10-line buffer where line 3 was added
// and one line ("gone") was removed between lines 5 and 6:
//
//	idx  0    1    2    3    4    5    6     7    8    9    10
//	row  ctx0 ctx1 ctx2 add3 ctx4 ctx5 del   ctx6 ctx7 ctx8 ctx9
func rowsFixture(t *testing.T) (cur []string, rows []diffRow, adds, dels int) {
	t.Helper()
	for i := 0; i < 10; i++ {
		cur = append(cur, fmt.Sprintf("l%d", i))
	}
	old := append([]string(nil), cur[:3]...) // l0..l2 (l3 not yet present)
	old = append(old, cur[4:6]...)           // l4, l5
	old = append(old, "gone")                // removed line
	old = append(old, cur[6:]...)            // l6..l9
	rows, adds, dels = buildDiffRows(old, cur)
	if adds != 1 || dels != 1 {
		t.Fatalf("fixture diff: adds=%d dels=%d, want 1/1", adds, dels)
	}
	return cur, rows, adds, dels
}

func TestBuildDiffRows(t *testing.T) {
	_, rows, _, _ := rowsFixture(t)
	wantKinds := []diffRowKind{diffCtx, diffCtx, diffCtx, diffAdd, diffCtx, diffCtx, diffDel, diffCtx, diffCtx, diffCtx, diffCtx}
	wantBuf := []int{0, 1, 2, 3, 4, 5, 6, 6, 7, 8, 9} // del carries its successor's line
	if len(rows) != len(wantKinds) {
		t.Fatalf("rows = %d, want %d", len(rows), len(wantKinds))
	}
	for i, row := range rows {
		if row.kind != wantKinds[i] || row.bufLine != wantBuf[i] {
			t.Fatalf("row %d = {kind %d, bufLine %d}, want {%d, %d}", i, row.kind, row.bufLine, wantKinds[i], wantBuf[i])
		}
	}
	if rows[6].text != "gone" {
		t.Fatalf("del row text = %q", rows[6].text)
	}
}

func TestBuildDiffRowsDelAtEOF(t *testing.T) {
	rows, adds, dels := []diffRow{}, 0, 0
	rows, adds, dels = buildDiffRows([]string{"a", "b", "tail"}, []string{"a", "b"})
	if adds != 0 || dels != 1 {
		t.Fatalf("adds=%d dels=%d", adds, dels)
	}
	last := rows[len(rows)-1]
	if last.kind != diffDel || last.bufLine != 1 {
		t.Fatalf("EOF del row = {kind %d, bufLine %d}, want clamped to last buffer line", last.kind, last.bufLine)
	}
}

func TestDiffRowMapping(t *testing.T) {
	_, rows, _, _ := rowsFixture(t)
	// Every buffer line maps to its exact ctx/add row, skipping the del row
	// that shares bufLine 6.
	wantRow := map[int]int{0: 0, 1: 1, 2: 2, 3: 3, 4: 4, 5: 5, 6: 7, 7: 8, 8: 9, 9: 10}
	for line, want := range wantRow {
		if got := diffRowForBufLine(rows, line); got != want {
			t.Fatalf("diffRowForBufLine(%d) = %d, want %d", line, got, want)
		}
	}
	if got := diffRowForBufLine(rows, 99); got != len(rows)-1 {
		t.Fatalf("out-of-range line should clamp to the last row, got %d", got)
	}
	if got := diffRowBufLine(rows, 6); got != 6 {
		t.Fatalf("del row should map to its successor, got %d", got)
	}
	if got := diffRowBufLine(rows, -5); got != 0 {
		t.Fatalf("cursor clamp failed, got %d", got)
	}
}

// diffFixture opens main.go with a known 10-line buffer, forces gitRepo, and
// lands a synthetic diff (no git subprocess involved).
func diffFixture(t *testing.T) (Model, []diffRow) {
	t.Helper()
	m, root := newTestModel(t, nil)
	cur, rows, adds, dels := rowsFixture(t)
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Join(cur, "\n")+"\n"), 0o644))
	m = m.openFileAt("main.go")
	m.gitRepo = true
	// Buffer ends with a trailing empty line (newline at EOF); trim it so the
	// buffer matches the fixture rows exactly.
	m.edit.lines = m.edit.lines[:10]
	next, cmd := m.enterDiff("")
	m = next.(Model)
	if cmd == nil || m.mode != modeDiff || !m.diffLoading {
		t.Fatalf("enterDiff: mode=%v loading=%v cmd=%v", m.mode, m.diffLoading, cmd)
	}
	res, _ := m.Update(diffReadyMsg{gen: m.diffGen, rel: m.openRel, rows: rows, adds: adds, dels: dels})
	m = res.(Model)
	if m.diffLoading || len(m.diffRows) != len(rows) {
		t.Fatalf("diff did not land: loading=%v rows=%d", m.diffLoading, len(m.diffRows))
	}
	return m, rows
}

func TestDiffEntryStaysAtCurrentPlace(t *testing.T) {
	m, root := newTestModel(t, nil)
	cur, rows, adds, dels := rowsFixture(t)
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Join(cur, "\n")+"\n"), 0o644))
	m = m.openFileAt("main.go")
	m.gitRepo = true
	m.edit.lines = m.edit.lines[:10]
	m.edit.cy, m.edit.cx = 6, 2
	next, _ := m.enterDiff("")
	m = next.(Model)
	res, _ := m.Update(diffReadyMsg{gen: m.diffGen, rel: m.openRel, rows: rows, adds: adds, dels: dels})
	m = res.(Model)

	// Buffer line 6 sits after one add and one del row: its diff row is 7.
	if m.diffCursor != 7 {
		t.Fatalf("diffCursor = %d, want 7 (buffer line 6)", m.diffCursor)
	}
	if m.diffCx != 2 {
		t.Fatalf("diffCx = %d, want the edit column", m.diffCx)
	}
	// Everything fits in one page here, so the on-screen row math clamps to 0.
	if m.diffScrollY != 0 {
		t.Fatalf("diffScrollY = %d", m.diffScrollY)
	}
}

func TestDiffReadyEmptyReturnsToEdit(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.gitRepo = true
	next, _ := m.enterDiff("")
	m = next.(Model)
	res, _ := m.Update(diffReadyMsg{gen: m.diffGen, rel: m.openRel})
	m = res.(Model)
	if m.mode != modeEdit || m.notice != "git diff: no changes" {
		t.Fatalf("mode=%v notice=%q", m.mode, m.notice)
	}
}

func TestDiffReadyErrorReturnsToEdit(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.gitRepo = true
	next, _ := m.enterDiff("nope")
	m = next.(Model)
	res, _ := m.Update(diffReadyMsg{gen: m.diffGen, rel: m.openRel, err: "git diff: bad revision: nope"})
	m = res.(Model)
	if m.mode != modeEdit || !strings.Contains(m.errText, "bad revision") {
		t.Fatalf("mode=%v errText=%q", m.mode, m.errText)
	}
}

func TestDiffReadyStaleGenDropped(t *testing.T) {
	m, rows := diffFixture(t)
	res, _ := m.Update(diffReadyMsg{gen: m.diffGen - 1, rel: m.openRel, rows: rows[:1], adds: 1})
	m = res.(Model)
	if len(m.diffRows) != len(rows) {
		t.Fatal("stale diffReadyMsg must not replace the landed rows")
	}
}

func TestDiffModeIsReadOnly(t *testing.T) {
	m, _ := diffFixture(t)
	before := m.edit.content()
	rev := m.edit.rev
	for _, msg := range []tea.KeyPressMsg{
		typeRune('x'), keyPress(tea.KeyEnter), keyPress(tea.KeyBackspace),
		ctrlKey('s'), ctrlKey('z'), keyPress(tea.KeySpace), keyPress(tea.KeyTab),
	} {
		m = key(m, msg)
	}
	if m.edit.content() != before || m.edit.rev != rev {
		t.Fatal("diff review must not reach the buffer")
	}
	if m.mode != modeDiff {
		t.Fatalf("mode = %v, want modeDiff", m.mode)
	}
	res, _ := m.handlePaste("pasted")
	m = res.(Model)
	if m.edit.content() != before {
		t.Fatal("paste must be inert in diff review")
	}
}

func TestDiffNavigationAndEsc(t *testing.T) {
	m, rows := diffFixture(t)
	m.diffCursor, m.diffCx, m.diffScrollY = 0, 0, 0
	for i := 0; i < 6; i++ {
		m = key(m, keyPress(tea.KeyDown))
	}
	if m.diffCursor != 6 || m.diffRows[6].kind != diffDel {
		t.Fatalf("cursor = %d (kind %d), want the del row", m.diffCursor, m.diffRows[m.diffCursor].kind)
	}
	// diffCx clamps to the del row's text ("gone").
	m.diffCx = 99
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyUp))
	if m.diffCx > len([]rune("gone")) {
		t.Fatalf("diffCx = %d, not clamped", m.diffCx)
	}

	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit {
		t.Fatalf("esc should land in edit mode, got %v", m.mode)
	}
	// The del row maps to its successor, buffer line 6.
	if m.edit.cy != 6 {
		t.Fatalf("edit.cy = %d, want 6", m.edit.cy)
	}
	if len(m.diffRows) != 0 || m.diffBase != "" {
		t.Fatal("esc should clear diff state")
	}
	_ = rows
}

func TestDiffShiftJumpsBetweenHunks(t *testing.T) {
	m, _ := diffFixture(t) // hunk starts: row 3 (add), row 6 (del)
	m.diffCursor, m.diffCx, m.diffScrollY = 0, 0, 0

	m = key(m, shiftKey(tea.KeyDown))
	if m.diffCursor != 3 {
		t.Fatalf("shift+down: cursor = %d, want hunk 1 start (3)", m.diffCursor)
	}
	m = key(m, shiftKey(tea.KeyDown))
	if m.diffCursor != 6 {
		t.Fatalf("shift+down: cursor = %d, want hunk 2 start (6)", m.diffCursor)
	}
	m = key(m, shiftKey(tea.KeyDown)) // no hunk below: inert
	if m.diffCursor != 6 {
		t.Fatalf("shift+down past the last hunk moved to %d", m.diffCursor)
	}
	m = key(m, shiftKey(tea.KeyUp))
	if m.diffCursor != 3 {
		t.Fatalf("shift+up: cursor = %d, want hunk 1 start (3)", m.diffCursor)
	}
	m = key(m, shiftKey(tea.KeyUp)) // no hunk above: inert
	if m.diffCursor != 3 {
		t.Fatalf("shift+up past the first hunk moved to %d", m.diffCursor)
	}

	// A del+add run is one hunk: rows ctx del del add add ctx, start at 1.
	// From inside it, shift+up lands on its own start.
	rows, _, _ := buildDiffRows([]string{"a", "x", "y", "d"}, []string{"a", "b", "c", "d"})
	m.diffRows = rows
	m.diffCursor = 3
	m = key(m, shiftKey(tea.KeyUp))
	if m.diffCursor != 1 {
		t.Fatalf("shift+up inside a hunk: cursor = %d, want its start (1)", m.diffCursor)
	}
	m = key(m, shiftKey(tea.KeyDown)) // the same hunk's tail is not a new hunk
	if m.diffCursor != 1 {
		t.Fatalf("shift+down with no hunk below moved to %d", m.diffCursor)
	}

	// Inert while the diff is still loading.
	m.diffRows = nil
	m.diffCursor = 0
	m = key(m, shiftKey(tea.KeyDown))
	if m.diffCursor != 0 {
		t.Fatalf("shift+down while loading moved to %d", m.diffCursor)
	}
}

func TestDiffEscWhileLoading(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.gitRepo = true
	m.edit.cy = 2
	next, _ := m.enterDiff("")
	m = next.(Model)
	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit || m.edit.cy != 2 {
		t.Fatalf("esc during load: mode=%v cy=%d", m.mode, m.edit.cy)
	}
}

func TestDiffCtrlJOnDelRowRefuses(t *testing.T) {
	m, _ := diffFixture(t)
	m.diffCursor = 6 // the del row
	m = key(m, ctrlKey('j'))
	if m.errText != "cannot jump from a removed line" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.mode != modeDiff || len(m.diffRows) == 0 {
		t.Fatal("refused jump must keep the review intact")
	}
}

func TestDiffJumpAndCtrlOReturnsToDiff(t *testing.T) {
	m, root := newTestModel(t, nil)
	// Buffer line 1 references a real file — the path-jump ladder resolves it
	// without any LSP.
	content := "package main\n// see lib/util.ts\nfunc main() {}\n"
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0o644))
	m = m.openFileAt("main.go")
	m.gitRepo = true
	next, _ := m.enterDiff("abc123")
	m = next.(Model)
	rows, adds, dels := buildDiffRows([]string{"package main", "func main() {}", ""}, m.edit.lines)
	res, _ := m.Update(diffReadyMsg{gen: m.diffGen, rel: m.openRel, rows: rows, adds: adds, dels: dels})
	m = res.(Model)
	if m.mode != modeDiff {
		t.Fatalf("fixture: mode = %v, err=%q", m.mode, m.errText)
	}

	// Review cursor on the added comment row, column inside "lib/util.ts".
	m.diffCursor = diffRowForBufLine(m.diffRows, 1)
	m.diffCx = 10
	savedCursor := m.diffCursor
	m = key(m, ctrlKey('j'))
	if m.openRel != "lib/util.ts" || m.mode != modeEdit {
		t.Fatalf("jump failed: open=%q mode=%v err=%q", m.openRel, m.mode, m.errText)
	}
	if len(m.diffRows) != 0 {
		t.Fatal("landing a jump must clear the diff view model")
	}
	if len(m.jumpStack) != 1 || !m.jumpStack[0].inDiff || m.jumpStack[0].diffBase != "abc123" {
		t.Fatalf("jump frame = %+v", m.jumpStack)
	}

	// Ctrl+O: back to main.go, re-entering diff review with a recompute.
	res, cmd := m.Update(ctrlKey('o'))
	m = res.(Model)
	if m.openRel != "main.go" || m.mode != modeDiff || !m.diffLoading || !m.diffHasPending {
		t.Fatalf("ctrl+o: open=%q mode=%v loading=%v pending=%v", m.openRel, m.mode, m.diffLoading, m.diffHasPending)
	}
	if cmd == nil {
		t.Fatal("ctrl+o to a diff frame must fire the recompute cmd")
	}
	if m.diffBase != "abc123" {
		t.Fatalf("diffBase = %q, want the original revision", m.diffBase)
	}
	// The recomputed diff lands: the review position comes back (clamped).
	res, _ = m.Update(diffReadyMsg{gen: m.diffGen, rel: m.openRel, rows: rows, adds: adds, dels: dels})
	m = res.(Model)
	if m.diffCursor != savedCursor {
		t.Fatalf("diffCursor = %d, want restored %d", m.diffCursor, savedCursor)
	}
}

// TestRenderDiffSmoke renders the full frame in diff mode: the removed line's
// text must appear (it exists nowhere in the buffer), the status line must
// show the mode, and nothing may panic across cursor positions.
func TestRenderDiffSmoke(t *testing.T) {
	m, rows := diffFixture(t)
	for _, cursor := range []int{0, 3, 6, len(rows) - 1} {
		m.diffCursor = cursor
		// The cursor row renders per-rune (ANSI between characters), so
		// assertions go against the stripped frame.
		frame := ansi.Strip(m.render())
		if !strings.Contains(frame, "gone") {
			t.Fatal("removed line text missing from the diff view")
		}
		if !strings.Contains(frame, "@diff") {
			t.Fatal("diff status line missing")
		}
		// Gutter markers: added rows carry "N+│", removed rows a blank
		// number with "-│" (fixture: add row is buffer line 4 → "4+│").
		if !strings.Contains(frame, "4+│") {
			t.Fatal("added row's gutter + marker missing")
		}
		if !strings.Contains(frame, "-│") {
			t.Fatal("removed row's gutter - marker missing")
		}
	}
	// While loading there are no rows yet — must still render.
	m.diffRows, m.diffLoading = nil, true
	if !strings.Contains(ansi.Strip(m.render()), "computing diff…") {
		t.Fatal("loading placeholder missing")
	}
}

// Diff-view click geometry: the 10-line fixture buffer keeps gutterWidth=2,
// so like edit mode (see mouse_test.go) content starts at column 31, row 4.
func TestDiffClickPlacesCursor(t *testing.T) {
	m, _ := diffFixture(t)
	m.diffCursor, m.diffCx, m.diffScrollY = 0, 0, 0

	// Click the del row (display row 6), rune column 2 of "gone".
	m = click(m, testTextX+2, testTextY+6)
	if m.diffCursor != 6 || m.diffCx != 2 {
		t.Fatalf("cursor = (%d, %d), want (6, 2)", m.diffCursor, m.diffCx)
	}
	if m.mode != modeDiff {
		t.Fatalf("mode = %v", m.mode)
	}
	// A gutter click lands on the row at column 0; columns clamp to row end.
	m = click(m, testTextX-3, testTextY+3)
	if m.diffCursor != 3 || m.diffCx != 0 {
		t.Fatalf("gutter click = (%d, %d), want (3, 0)", m.diffCursor, m.diffCx)
	}
	m = click(m, testTextX+50, testTextY+1)
	if m.diffCursor != 1 || m.diffCx != len([]rune("l1")) {
		t.Fatalf("beyond-EOL click = (%d, %d), want clamped to line end", m.diffCursor, m.diffCx)
	}
	// A click below the last diff row is a no-op.
	before := m.diffCursor
	m = click(m, testTextX, testTextY+20)
	if m.diffCursor != before {
		t.Fatal("click below the diff must not move the cursor")
	}
}

func TestDiffClickPagesAtEdges(t *testing.T) {
	m, root := newTestModel(t, nil)
	// A buffer taller than the pane so the viewport actually pages.
	var cur []string
	for i := 0; i < 80; i++ {
		cur = append(cur, fmt.Sprintf("line %d", i))
	}
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Join(cur, "\n")+"\n"), 0o644))
	m = m.openFileAt("main.go")
	m.gitRepo = true
	m.edit.lines = m.edit.lines[:80]
	next, _ := m.enterDiff("")
	m = next.(Model)
	rows, adds, dels := buildDiffRows(append([]string{"old first"}, cur[1:]...), m.edit.lines)
	res, _ := m.Update(diffReadyMsg{gen: m.diffGen, rel: m.openRel, rows: rows, adds: adds, dels: dels})
	m = res.(Model)

	h := m.contentHeight() + 1
	m.diffCursor, m.diffScrollY = 40, 30 // viewport [30, 30+h)
	bottom := 30 + h - 1
	m = click(m, testTextX, testTextY+(bottom-30))
	if m.diffCursor != bottom || m.diffScrollY != bottom {
		t.Fatalf("bottom-row click: cursor=%d scroll=%d, want both %d", m.diffCursor, m.diffScrollY, bottom)
	}
	m = click(m, testTextX, testTextY) // top visible row → page up
	top := bottom
	if m.diffCursor != top || m.diffScrollY != max(0, top-h+1) {
		t.Fatalf("top-row click: cursor=%d scroll=%d, want cursor %d at the bottom", m.diffCursor, m.diffScrollY, top)
	}
}

func TestDiffCtrlClickOnDelRowRefuses(t *testing.T) {
	m, _ := diffFixture(t)
	m.diffCursor, m.diffScrollY = 0, 0
	next, cmd := ctrlClick(m, testTextX, testTextY+6) // the del row
	m = next
	if cmd != nil {
		t.Fatal("del-row ctrl+click must not fire a jump cmd")
	}
	if m.errText != "cannot jump from a removed line" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.diffCursor != 6 {
		t.Fatalf("the click should still place the cursor, got %d", m.diffCursor)
	}
}

func TestExecGitDiffGuards(t *testing.T) {
	m, _ := newTestModel(t, nil)
	// No file open.
	res, _ := m.runExecCommand("git diff")
	if got := res.(Model); got.errText != "no file open" {
		t.Fatalf("errText = %q", got.errText)
	}
	// File open, but not a git repo (the temp project has no .git).
	m = m.openFileAt("main.go")
	m = key(m, ctrlKey('e'))
	m = runes(m, "git diff")
	m = key(m, keyPress(tea.KeyEnter))
	if m.errText != "file is not in a git repository" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.mode != modeExec {
		t.Fatalf("errors should stay in the exec bar, got %v", m.mode)
	}
}

// TestComputeDiffCmdIntegration exercises the real git plumbing end-to-end in
// a throwaway repo. Skipped when git is unavailable.
func TestComputeDiffCmdIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m, root := newTestModel(t, nil)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "main.go")
	git("commit", "-q", "-m", "init")

	m = m.openFileAt("main.go")
	m.gitRepo = true
	m.edit.lines[0] = "package changed" // unsaved buffer edit must show up

	next, cmd := m.enterDiff("")
	m = next.(Model)
	res, _ := m.Update(cmd())
	m = res.(Model)
	if m.mode != modeDiff || m.diffAdds != 1 || m.diffDels != 1 || m.diffNewFile {
		t.Fatalf("mode=%v adds=%d dels=%d newFile=%v err=%q", m.mode, m.diffAdds, m.diffDels, m.diffNewFile, m.errText)
	}

	// A bad revision reports git's message and returns to edit mode.
	next, cmd = m.exitDiff().enterDiff("no-such-rev")
	m = next.(Model)
	res, _ = m.Update(cmd())
	m = res.(Model)
	if m.mode != modeEdit || m.errText == "" {
		t.Fatalf("bad revision: mode=%v err=%q", m.mode, m.errText)
	}

	// An untracked file diffs as all-added.
	must(t, os.WriteFile(filepath.Join(root, "fresh.go"), []byte("package fresh\nvar X = 1\n"), 0o644))
	m = m.openFileAt("fresh.go")
	m.gitRepo = true
	next, cmd = m.enterDiff("")
	m = next.(Model)
	res, _ = m.Update(cmd())
	m = res.(Model)
	if m.mode != modeDiff || !m.diffNewFile || m.diffDels != 0 || m.diffAdds != len(m.edit.lines) {
		t.Fatalf("untracked: mode=%v newFile=%v adds=%d dels=%d", m.mode, m.diffNewFile, m.diffAdds, m.diffDels)
	}
	for _, row := range m.diffRows {
		if row.kind != diffAdd {
			t.Fatal("untracked file must render all-added")
		}
	}
}

func TestEnterDiffRejectsDashRevision(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte("hello\n"), 0o644))
	m = m.openFileAt("main.go")
	m.gitRepo = true
	next, cmd := m.enterDiff("-Rfoo")
	m = next.(Model)
	if cmd != nil {
		t.Fatal("a dash revision must not spawn git")
	}
	if m.mode == modeDiff {
		t.Fatal("a dash revision must keep the current mode")
	}
	if !strings.Contains(m.errText, "bad revision") {
		t.Fatalf("errText = %q, want a bad-revision message", m.errText)
	}
}

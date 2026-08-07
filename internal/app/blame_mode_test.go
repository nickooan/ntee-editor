package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const (
	testSHA1 = "1111111111111111111111111111111111111111"
	testSHA2 = "2222222222222222222222222222222222222222"
)

// blamePorcelainFixture is git blame --porcelain output for five lines:
// sha1 sha1 sha2 sha1 zero. sha1's third appearance and sha2's carry no
// attribute lines (porcelain sends them only on the first appearance), so the
// parse must resolve them from the cache. The zero-sha line carries git's
// --contents placeholder author, which must never surface.
func blamePorcelainFixture() string {
	return strings.Join([]string{
		testSHA1 + " 1 1 2",
		"author ann",
		"author-mail <ann@x>",
		"author-time 1700000000",
		"author-tz +0000",
		"committer ann",
		"summary first",
		"filename main.go",
		"\tline one",
		testSHA1 + " 2 2",
		"\tline two",
		testSHA2 + " 3 3 1",
		"author bob smith",
		"author-mail <bob@x>",
		"author-time 1710000000",
		"author-tz +0000",
		"committer bob smith",
		"summary second",
		"filename main.go",
		"\tline three",
		testSHA1 + " 3 4 1",
		"\tline four",
		zeroSHA + " 5 5 1",
		"author External file (--contents)",
		"author-mail <external.file@localhost>",
		"author-time 1720000000",
		"author-tz +0000",
		"committer External file (--contents)",
		"summary Version of main.go from standard input",
		"filename main.go",
		"\tline five",
		"",
	}, "\n")
}

func TestParseBlamePorcelain(t *testing.T) {
	rows, err := parseBlamePorcelain([]byte(blamePorcelainFixture()))
	if err != nil {
		t.Fatal(err)
	}
	date1 := time.Unix(1700000000, 0).Format("2006-01-02")
	date2 := time.Unix(1710000000, 0).Format("2006-01-02")
	want := []blameRow{
		{author: "ann", date: date1, group: 0},
		{author: "ann", date: date1, group: 0},
		{author: "bob smith", date: date2, group: 1},
		{author: "ann", date: date1, group: 2}, // sha changed back: a new group
		{uncommitted: true, group: 3},          // zero sha: placeholder author discarded
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for i, row := range rows {
		if row != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, row, want[i])
		}
	}
}

func TestParseBlamePorcelainMalformed(t *testing.T) {
	if _, err := parseBlamePorcelain([]byte("\tcontent before any header\n")); err == nil {
		t.Fatal("content line before a header must error")
	}
}

func TestBlameAuthorWidth(t *testing.T) {
	if got := blameAuthorWidth(nil); got != 1 {
		t.Fatalf("empty rows: width = %d, want 1", got)
	}
	rows := []blameRow{{author: "ann"}, {author: "bob smith"}}
	if got := blameAuthorWidth(rows); got != len("bob smith") {
		t.Fatalf("width = %d, want %d", got, len("bob smith"))
	}
	// The uncommitted placeholder participates in the measurement.
	rows = append(rows, blameRow{uncommitted: true})
	if got := blameAuthorWidth(rows); got != len(blameUncommittedLabel) {
		t.Fatalf("width = %d, want %d", got, len(blameUncommittedLabel))
	}
	rows = append(rows, blameRow{author: "a very long author name indeed"})
	if got := blameAuthorWidth(rows); got != blameAuthorCap {
		t.Fatalf("width = %d, want the cap %d", got, blameAuthorCap)
	}
}

// blameRowsFixture annotates the 10-line buffer: lines 0-4 by ann, 5-7 by
// bob, 8-9 uncommitted.
func blameRowsFixture() []blameRow {
	rows := make([]blameRow, 10)
	for i := 0; i < 5; i++ {
		rows[i] = blameRow{author: "ann", date: "2026-01-02", group: 0}
	}
	for i := 5; i < 8; i++ {
		rows[i] = blameRow{author: "bob", date: "2026-03-04", group: 1}
	}
	for i := 8; i < 10; i++ {
		rows[i] = blameRow{uncommitted: true, group: 2}
	}
	return rows
}

// blameFixture opens main.go with a known 10-line buffer, forces gitRepo, and
// lands a synthetic blame (no git subprocess involved).
func blameFixture(t *testing.T) (Model, []blameRow) {
	t.Helper()
	m, root := newTestModel(t, nil)
	var cur []string
	for i := 0; i < 10; i++ {
		cur = append(cur, fmt.Sprintf("l%d", i))
	}
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Join(cur, "\n")+"\n"), 0o644))
	m = m.openFileAt("main.go")
	m.gitRepo = true
	m.edit.lines = m.edit.lines[:10]
	rows := blameRowsFixture()
	next, cmd := m.enterBlame()
	m = next.(Model)
	if cmd == nil || m.mode != modeBlame || !m.blameLoading {
		t.Fatalf("enterBlame: mode=%v loading=%v cmd=%v", m.mode, m.blameLoading, cmd)
	}
	res, _ := m.Update(blameReadyMsg{gen: m.blameGen, rel: m.openRel, rows: rows, authorW: blameAuthorWidth(rows)})
	m = res.(Model)
	if m.blameLoading || len(m.blameRows) != len(rows) {
		t.Fatalf("blame did not land: loading=%v rows=%d", m.blameLoading, len(m.blameRows))
	}
	return m, rows
}

func TestBlameEntryStaysAtCurrentPlace(t *testing.T) {
	m, root := newTestModel(t, nil)
	var cur []string
	for i := 0; i < 10; i++ {
		cur = append(cur, fmt.Sprintf("l%d", i))
	}
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Join(cur, "\n")+"\n"), 0o644))
	m = m.openFileAt("main.go")
	m.gitRepo = true
	m.edit.lines = m.edit.lines[:10]
	m.edit.cy, m.edit.cx = 6, 2
	next, _ := m.enterBlame()
	m = next.(Model)
	rows := blameRowsFixture()
	res, _ := m.Update(blameReadyMsg{gen: m.blameGen, rel: m.openRel, rows: rows, authorW: blameAuthorWidth(rows)})
	m = res.(Model)

	// Rows map 1:1: the cursor stays on the same line and column.
	if m.blameCursor != 6 || m.blameCx != 2 {
		t.Fatalf("cursor = (%d, %d), want (6, 2)", m.blameCursor, m.blameCx)
	}
	if m.blameScrollY != 0 {
		t.Fatalf("blameScrollY = %d", m.blameScrollY)
	}
}

func TestBlameReadyErrorReturnsToEdit(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.gitRepo = true
	next, _ := m.enterBlame()
	m = next.(Model)
	res, _ := m.Update(blameReadyMsg{gen: m.blameGen, rel: m.openRel, err: "git blame: boom"})
	m = res.(Model)
	if m.mode != modeEdit || m.errText != "git blame: boom" {
		t.Fatalf("mode=%v errText=%q", m.mode, m.errText)
	}
	if len(m.blameRows) != 0 {
		t.Fatal("error must clear blame state")
	}
}

func TestBlameReadyStaleGenDropped(t *testing.T) {
	m, rows := blameFixture(t)
	res, _ := m.Update(blameReadyMsg{gen: m.blameGen - 1, rel: m.openRel, rows: rows[:1], authorW: 3})
	m = res.(Model)
	if len(m.blameRows) != len(rows) {
		t.Fatal("stale blameReadyMsg must not replace the landed rows")
	}
}

func TestBlameModeIsReadOnly(t *testing.T) {
	m, _ := blameFixture(t)
	before := m.edit.content()
	rev := m.edit.rev
	for _, msg := range []tea.KeyPressMsg{
		typeRune('x'), keyPress(tea.KeyEnter), keyPress(tea.KeyBackspace),
		ctrlKey('s'), ctrlKey('z'), keyPress(tea.KeySpace), keyPress(tea.KeyTab),
	} {
		m = key(m, msg)
	}
	if m.edit.content() != before || m.edit.rev != rev {
		t.Fatal("blame view must not reach the buffer")
	}
	if m.mode != modeBlame {
		t.Fatalf("mode = %v, want modeBlame", m.mode)
	}
	res, _ := m.handlePaste("pasted")
	m = res.(Model)
	if m.edit.content() != before {
		t.Fatal("paste must be inert in blame view")
	}
}

func TestBlameNavigationAndEsc(t *testing.T) {
	m, _ := blameFixture(t)
	m.blameCursor, m.blameCx, m.blameScrollY = 0, 0, 0
	for i := 0; i < 6; i++ {
		m = key(m, keyPress(tea.KeyDown))
	}
	if m.blameCursor != 6 {
		t.Fatalf("cursor = %d, want 6", m.blameCursor)
	}
	m.blameCx = 99
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyUp))
	if m.blameCx > len([]rune("l6")) {
		t.Fatalf("blameCx = %d, not clamped", m.blameCx)
	}

	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit {
		t.Fatalf("esc should land in edit mode, got %v", m.mode)
	}
	if m.edit.cy != 6 {
		t.Fatalf("edit.cy = %d, want 6", m.edit.cy)
	}
	if len(m.blameRows) != 0 {
		t.Fatal("esc should clear blame state")
	}
}

func TestBlameShiftJumpsBetweenGroups(t *testing.T) {
	m, _ := blameFixture(t) // group starts: rows 0, 5, 8
	m.blameCursor, m.blameCx, m.blameScrollY = 0, 0, 0

	m = key(m, shiftKey(tea.KeyDown))
	if m.blameCursor != 5 {
		t.Fatalf("shift+down: cursor = %d, want group 1 start (5)", m.blameCursor)
	}
	m = key(m, shiftKey(tea.KeyDown))
	if m.blameCursor != 8 {
		t.Fatalf("shift+down: cursor = %d, want group 2 start (8)", m.blameCursor)
	}
	m = key(m, shiftKey(tea.KeyDown)) // no group below: inert
	if m.blameCursor != 8 {
		t.Fatalf("shift+down past the last group moved to %d", m.blameCursor)
	}
	m = key(m, shiftKey(tea.KeyUp))
	if m.blameCursor != 5 {
		t.Fatalf("shift+up: cursor = %d, want group 1 start (5)", m.blameCursor)
	}
	// From inside a group, shift+up lands on its own start.
	m.blameCursor = 7
	m = key(m, shiftKey(tea.KeyUp))
	if m.blameCursor != 5 {
		t.Fatalf("shift+up inside a group: cursor = %d, want its start (5)", m.blameCursor)
	}

	// Inert while the blame is still loading.
	m.blameRows = nil
	m.blameCursor = 0
	m = key(m, shiftKey(tea.KeyDown))
	if m.blameCursor != 0 {
		t.Fatalf("shift+down while loading moved to %d", m.blameCursor)
	}
}

func TestBlameEscWhileLoading(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("main.go")
	m.gitRepo = true
	m.edit.cy = 2
	next, _ := m.enterBlame()
	m = next.(Model)
	m = key(m, keyPress(tea.KeyEsc))
	if m.mode != modeEdit || m.edit.cy != 2 {
		t.Fatalf("esc during load: mode=%v cy=%d", m.mode, m.edit.cy)
	}
}

// TestRenderBlameSmoke renders the full frame in blame mode: authors and
// dates must appear in the gutter, uncommitted rows show the dimmed
// placeholder, the status line shows the mode, and nothing may panic across
// cursor positions.
func TestRenderBlameSmoke(t *testing.T) {
	m, rows := blameFixture(t)
	for _, cursor := range []int{0, 5, 8, len(rows) - 1} {
		m.blameCursor = cursor
		frame := ansi.Strip(m.render())
		for _, want := range []string{"@blame", "ann", "bob", "2026-01-02", "2026-03-04", blameUncommittedLabel} {
			if !strings.Contains(frame, want) {
				t.Fatalf("%q missing from the blame view", want)
			}
		}
		// No line numbers: the edit gutter's "NN │" never renders in blame
		// mode (dates end in digits but sit one space away from the │).
		if strings.Contains(frame, "1 │") {
			t.Fatal("line numbers must not render in blame mode")
		}
	}
	// While loading there are no rows yet — must still render.
	m.blameRows, m.blameLoading = nil, true
	if !strings.Contains(ansi.Strip(m.render()), "computing blame…") {
		t.Fatal("loading placeholder missing")
	}
}

// Blame-view click geometry: same chrome as the edit view (mouse_test.go),
// but the content column starts after the author+date gutter instead of the
// 2-digit line-number gutter.
func TestBlameClickPlacesCursor(t *testing.T) {
	m, _ := blameFixture(t)
	m.blameCursor, m.blameCx, m.blameScrollY = 0, 0, 0
	// testTextX is edit mode's content start (gutter 2+3); rebase it onto the
	// blame gutter.
	blameTextX := testTextX - (2 + 3) + m.blameGutterWidth() + 3

	m = click(m, blameTextX+1, testTextY+6)
	if m.blameCursor != 6 || m.blameCx != 1 {
		t.Fatalf("cursor = (%d, %d), want (6, 1)", m.blameCursor, m.blameCx)
	}
	if m.mode != modeBlame {
		t.Fatalf("mode = %v", m.mode)
	}
	// A gutter click lands on the row at column 0; columns clamp to row end.
	m = click(m, blameTextX-4, testTextY+3)
	if m.blameCursor != 3 || m.blameCx != 0 {
		t.Fatalf("gutter click = (%d, %d), want (3, 0)", m.blameCursor, m.blameCx)
	}
	m = click(m, blameTextX+30, testTextY+1)
	if m.blameCursor != 1 || m.blameCx != len([]rune("l1")) {
		t.Fatalf("beyond-EOL click = (%d, %d), want clamped to line end", m.blameCursor, m.blameCx)
	}
	// A click below the last row is a no-op.
	before := m.blameCursor
	m = click(m, blameTextX, testTextY+20)
	if m.blameCursor != before {
		t.Fatal("click below the blame rows must not move the cursor")
	}
}

func TestExecGitBlameGuards(t *testing.T) {
	m, _ := newTestModel(t, nil)
	// No file open.
	res, _ := m.runExecCommand("git blame")
	if got := res.(Model); got.errText != "no file open" {
		t.Fatalf("errText = %q", got.errText)
	}
	// File open, but not a git repo (the temp project has no .git).
	m = m.openFileAt("main.go")
	m = key(m, ctrlKey('e'))
	m = runes(m, "git blame")
	m = key(m, keyPress(tea.KeyEnter))
	if m.errText != "not a git repository" {
		t.Fatalf("errText = %q", m.errText)
	}
	if m.mode != modeExec {
		t.Fatalf("errors should stay in the exec bar, got %v", m.mode)
	}
	// A stray argument is rejected loudly.
	m.gitRepo = true
	m.errText = ""
	res, _ = m.runExecCommand("git blame HEAD")
	if got := res.(Model); got.errText != "git blame takes no argument" {
		t.Fatalf("errText = %q", got.errText)
	}
}

func TestBlameJumpAndCtrlOReturnsToBlame(t *testing.T) {
	m, root := newTestModel(t, nil)
	// Buffer line 1 references a real file — the path-jump ladder resolves it
	// without any LSP.
	content := "package main\n// see lib/util.ts\nfunc main() {}\n"
	must(t, os.WriteFile(filepath.Join(root, "main.go"), []byte(content), 0o644))
	m = m.openFileAt("main.go")
	m.gitRepo = true
	next, _ := m.enterBlame()
	m = next.(Model)
	rows := make([]blameRow, len(m.edit.lines))
	for i := range rows {
		rows[i] = blameRow{author: "ann", date: "2026-01-02"}
	}
	res, _ := m.Update(blameReadyMsg{gen: m.blameGen, rel: m.openRel, rows: rows, authorW: 3})
	m = res.(Model)
	if m.mode != modeBlame {
		t.Fatalf("fixture: mode = %v, err=%q", m.mode, m.errText)
	}

	// Cursor on the comment row, column inside "lib/util.ts".
	m.blameCursor, m.blameCx = 1, 10
	m = key(m, ctrlKey('j'))
	if m.openRel != "lib/util.ts" || m.mode != modeEdit {
		t.Fatalf("jump failed: open=%q mode=%v err=%q", m.openRel, m.mode, m.errText)
	}
	if len(m.blameRows) != 0 {
		t.Fatal("landing a jump must clear the blame view model")
	}
	if len(m.jumpStack) != 1 || !m.jumpStack[0].inBlame || m.jumpStack[0].blameCursor != 1 {
		t.Fatalf("jump frame = %+v", m.jumpStack)
	}

	// Ctrl+O: back to main.go, re-entering blame with a recompute.
	res, cmd := m.Update(ctrlKey('o'))
	m = res.(Model)
	if m.openRel != "main.go" || m.mode != modeBlame || !m.blameLoading || !m.blameHasPending {
		t.Fatalf("ctrl+o: open=%q mode=%v loading=%v pending=%v", m.openRel, m.mode, m.blameLoading, m.blameHasPending)
	}
	if cmd == nil {
		t.Fatal("ctrl+o to a blame frame must fire the recompute cmd")
	}
	// The recomputed blame lands: the position comes back (clamped).
	res, _ = m.Update(blameReadyMsg{gen: m.blameGen, rel: m.openRel, rows: rows, authorW: 3})
	m = res.(Model)
	if m.blameCursor != 1 {
		t.Fatalf("blameCursor = %d, want restored 1", m.blameCursor)
	}
}

// TestComputeBlameCmdIntegration exercises the real git plumbing end-to-end
// in a throwaway repo. Skipped when git is unavailable.
func TestComputeBlameCmdIntegration(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	m, root := newTestModel(t, nil)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=tester"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "main.go")
	git("commit", "-q", "-m", "init")

	m = m.openFileAt("main.go")
	m.gitRepo = true
	m.edit.lines[0] = "package changed" // unsaved buffer edit must blame as uncommitted

	next, cmd := m.enterBlame()
	m = next.(Model)
	res, _ := m.Update(cmd())
	m = res.(Model)
	if m.mode != modeBlame || m.blameNewFile || m.errText != "" {
		t.Fatalf("mode=%v newFile=%v err=%q", m.mode, m.blameNewFile, m.errText)
	}
	if len(m.blameRows) != len(m.edit.lines) {
		t.Fatalf("rows = %d, want one per buffer line (%d)", len(m.blameRows), len(m.edit.lines))
	}
	if !m.blameRows[0].uncommitted {
		t.Fatal("the edited line must blame as uncommitted")
	}
	if row := m.blameRows[1]; row.uncommitted || row.author != "tester" || row.date == "" {
		t.Fatalf("committed line = %+v, want tester with a date", row)
	}
	// The buffer's trailing "" element pads as uncommitted.
	if !m.blameRows[len(m.blameRows)-1].uncommitted {
		t.Fatal("the trailing phantom line must blame as uncommitted")
	}

	// An untracked file blames as all-uncommitted (new file).
	must(t, os.WriteFile(filepath.Join(root, "fresh.go"), []byte("package fresh\nvar X = 1\n"), 0o644))
	m = m.exitBlame()
	m = m.openFileAt("fresh.go")
	m.gitRepo = true
	next, cmd = m.enterBlame()
	m = next.(Model)
	res, _ = m.Update(cmd())
	m = res.(Model)
	if m.mode != modeBlame || !m.blameNewFile {
		t.Fatalf("untracked: mode=%v newFile=%v err=%q", m.mode, m.blameNewFile, m.errText)
	}
	for _, row := range m.blameRows {
		if !row.uncommitted {
			t.Fatal("untracked file must blame as all-uncommitted")
		}
	}
}

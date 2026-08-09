package app

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// queryEnter types s into the query bar and presses Enter.
func queryEnter(m Model, s string) Model {
	m = runes(m, s)
	return key(m, keyPress(tea.KeyEnter))
}

// queryRm submits "<path> :rm" and confirms the modal — the two-step flow a
// destructive delete now requires.
func queryRm(t *testing.T, m Model, s string) Model {
	t.Helper()
	m = queryEnter(m, s)
	if m.confirmRm == "" {
		t.Fatalf(":rm %q should arm the confirmation modal (err=%q)", s, m.errText)
	}
	return key(m, keyPress(tea.KeyEnter))
}

func TestQueryMkdirCreatesAndEnters(t *testing.T) {
	m, root := newTestModel(t, nil)
	m = queryEnter(m, "lib :mkdir new/new1/new-dir")

	info, err := os.Stat(filepath.Join(root, "lib", "new", "new1", "new-dir"))
	if err != nil || !info.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
	if m.command != "lib/new/new1/new-dir/" || m.selectedCommand != m.command {
		t.Fatalf("bar should enter the new dir: command=%q selected=%q", m.command, m.selectedCommand)
	}
	if m.notice == "" || m.errText != "" {
		t.Fatalf("notice=%q errText=%q", m.notice, m.errText)
	}
}

func TestQueryTouchCreatesAndOpensInEdit(t *testing.T) {
	m, root := newTestModel(t, nil)
	m = queryEnter(m, "lib :touch new-path/file.ts")

	if _, err := os.Stat(filepath.Join(root, "lib", "new-path", "file.ts")); err != nil {
		t.Fatalf("file not created: %v", err)
	}
	if m.mode != modeEdit || m.openRel != "lib/new-path/file.ts" {
		t.Fatalf("should open the new file in edit mode: mode=%v openRel=%q err=%q", m.mode, m.openRel, m.errText)
	}
	if m.edit.content() != "" {
		t.Fatalf("new file must be empty, got %q", m.edit.content())
	}
}

func TestQueryTouchAtRootWithLeadingColon(t *testing.T) {
	m, root := newTestModel(t, nil)
	m = queryEnter(m, ":touch top.html")

	if _, err := os.Stat(filepath.Join(root, "top.html")); err != nil {
		t.Fatalf("root file not created: %v", err)
	}
	if m.mode != modeEdit || m.openRel != "top.html" {
		t.Fatalf("mode=%v openRel=%q", m.mode, m.openRel)
	}
}

func TestQueryTouchExistingOpensWithoutError(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = queryEnter(m, ":touch main.go") // fixture file already exists

	if m.errText != "" {
		t.Fatalf("touching an existing file must not error: %q", m.errText)
	}
	if m.mode != modeEdit || m.openRel != "main.go" {
		t.Fatalf("mode=%v openRel=%q", m.mode, m.openRel)
	}
	if m.edit.content() == "" {
		t.Fatal("existing content must be preserved")
	}
}

func TestQueryMkdirEscapeRejected(t *testing.T) {
	m, root := newTestModel(t, nil)
	m = queryEnter(m, "lib :mkdir ../../escape")

	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape")); err == nil {
		t.Fatal("escape dir must not be created outside root")
	}
	// The input parses as non-create (rel escapes) and falls through to
	// navigation — nothing created, no crash; the bar keeps the typed text.
	if m.mode != modeQuery {
		t.Fatalf("should stay in query mode, got %v", m.mode)
	}
}

// Non-fs ":" inputs still route to executeCommand (":recent" was removed as a
// verb — Ctrl+P is the same thing — so it now reports unknown there).
func TestQueryColonStillRoutesToExecuteCommand(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = runes(m, ":recent")
	next, _ := m.Update(keyPress(tea.KeyEnter))
	m = next.(Model)
	if m.fuzzyOpen {
		t.Fatal(":recent verb was removed and must not open the finder")
	}
	if m.errText != "unknown command :recent" {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestParseInlineFs(t *testing.T) {
	cases := []struct {
		in        string
		verb, rel string
		ok        bool
	}{
		{"src/acp/ :mkdir new-dir", "mkdir", "src/acp/new-dir", true},
		{"src/acp :mkdir a/b", "mkdir", "src/acp/a/b", true},
		{":touch f.html", "touch", "f.html", true},
		{"a/ :touch b/f.ts", "touch", "a/b/f.ts", true},
		{"src/test/and-test/test.ts :rm", "rm", "src/test/and-test/test.ts", true},
		{"src/test/and-test :rm", "rm", "src/test/and-test", true},
		{"src/test/and-test/ :rm", "rm", "src/test/and-test", true},
		{":mkdir", "", "", false},           // missing arg
		{":rm", "", "", false},              // would target the root
		{"a :rm b", "", "", false},          // rm takes no argument
		{":recent", "", "", false},          // other : commands untouched
		{"src/acp/", "", "", false},         // plain navigation
		{"a :mkdir ../../x", "", "", false}, // escapes root
		{":touch /abs", "", "", false},      // absolute
	}
	for _, c := range cases {
		verb, rel, ok := parseInlineFs(c.in)
		if verb != c.verb || rel != c.rel || ok != c.ok {
			t.Errorf("parseInlineFs(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, verb, rel, ok, c.verb, c.rel, c.ok)
		}
	}
}

func TestQueryRmFile(t *testing.T) {
	m, root := newTestModel(t, nil)
	m = queryRm(t, m, "lib/util.ts :rm")

	if _, err := os.Stat(filepath.Join(root, "lib", "util.ts")); !os.IsNotExist(err) {
		t.Fatalf("file should be gone, stat err=%v", err)
	}
	if m.command != "lib/" || m.selectedCommand != "lib/" {
		t.Fatalf("bar should move to the parent: command=%q selected=%q", m.command, m.selectedCommand)
	}
	if m.errText != "" || m.notice == "" {
		t.Fatalf("notice=%q errText=%q", m.notice, m.errText)
	}
}

func TestQueryRmDirRecursive(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.MkdirAll(filepath.Join(root, "lib", "sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "lib", "sub", "x.go"), []byte("x"), 0o644))

	m = queryRm(t, m, "lib :rm")
	if _, err := os.Stat(filepath.Join(root, "lib")); !os.IsNotExist(err) {
		t.Fatalf("dir subtree should be gone, stat err=%v", err)
	}
	if m.command != "" {
		t.Fatalf("removing a top-level dir should move the bar to the root, got %q", m.command)
	}
}

func TestQueryRmOpenFileClosesIt(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openFileAt("lib/util.ts")
	if m.openRel != "lib/util.ts" || len(m.tabs) == 0 {
		t.Fatalf("fixture: openRel=%q tabs=%v", m.openRel, m.tabs)
	}
	m.mode = modeQuery // the bar drives rm from query mode

	m = queryRm(t, m, "lib :rm")
	if m.openFile != nil || m.openRel != "" {
		t.Fatalf("open file under the removed dir must close: openRel=%q", m.openRel)
	}
	for _, tab := range m.tabs {
		if tab == "lib/util.ts" {
			t.Fatal("removed file must leave the tab list")
		}
	}
	// The recent-visit record (written by openFileAt) must be pruned too, so
	// the dead path doesn't linger in ntee-db.
	for _, f := range m.db.RecentFiles(0) {
		if f.Path == "lib/util.ts" {
			t.Fatal("removed file must leave the recents store")
		}
	}
}

func TestQueryRmMissingPathErrors(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = queryEnter(m, "nope/missing :rm")
	if m.errText == "" {
		t.Fatal("removing a nonexistent path must set errText")
	}
}

func TestQueryRmConfirmCancelAndSwallow(t *testing.T) {
	m, root := newTestModel(t, nil)
	target := filepath.Join(root, "lib", "util.ts")

	// Esc cancels: nothing deleted, modal closed, notice set.
	m = queryEnter(m, "lib/util.ts :rm")
	if m.confirmRm != "lib/util.ts" || m.confirmRmDir {
		t.Fatalf("confirm not armed: rm=%q dir=%v", m.confirmRm, m.confirmRmDir)
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.confirmRm != "" {
		t.Fatal("esc should close the modal")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("cancelled rm must not delete:", err)
	}
	if m.notice != "rm cancelled" {
		t.Fatalf("notice = %q", m.notice)
	}

	// Stray keys are swallowed while the modal is open (no typing leaks into
	// the query bar, no deletion). Reset the bar first — cancelling keeps its
	// text so the user can correct it.
	m.command, m.qCursor = "", 0
	m = queryEnter(m, "lib/util.ts :rm")
	before := m.command
	m = runes(m, "zz")
	if m.confirmRm == "" || m.command != before {
		t.Fatalf("stray keys must be swallowed: rm=%q command=%q", m.confirmRm, m.command)
	}
	// A directory arms with the directory wording flag.
	m = key(m, keyPress(tea.KeyEsc))
	m.command, m.qCursor = "", 0
	m = queryEnter(m, "lib :rm")
	if m.confirmRm != "lib" || !m.confirmRmDir {
		t.Fatalf("dir confirm: rm=%q dir=%v", m.confirmRm, m.confirmRmDir)
	}
	// y confirms like enter.
	m = key(m, typeRune('y'))
	if m.confirmRm != "" {
		t.Fatal("y should resolve the modal")
	}
	if _, err := os.Stat(filepath.Join(root, "lib")); !os.IsNotExist(err) {
		t.Fatalf("y should delete the directory, stat err=%v", err)
	}
}

// The sidebar must keep targeting the typed path while an inline command
// suffix is pending — "lib/util.ts :rm" highlights the file, not "lib/".
func TestInlineFsSuffixKeepsSidebarTarget(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = runes(m, "lib/util.ts")
	entries := m.treeEntries()
	before := m.highlightedEntryIndex(entries)
	if before < 0 || entries[before].CommandValue != "lib/util.ts" {
		t.Fatalf("typed path should highlight the file: idx=%d", before)
	}

	m = runes(m, " :rm")
	m.invalidateTreeEntries() // fresh walk for the new sidebar key
	entries = m.treeEntries()
	after := m.highlightedEntryIndex(entries)
	if after < 0 || entries[after].CommandValue != "lib/util.ts" {
		got := "<none>"
		if after >= 0 {
			got = entries[after].CommandValue
		}
		t.Fatalf("pending :rm must keep the file highlighted, got %q", got)
	}

	// A non-verb suffix is not a command — the raw text flows through and the
	// ancestor fallback still applies (unchanged behavior).
	if base, ok := inlineFsPathPrefix("lib/util.ts :nope"); ok {
		t.Fatalf("non-verb suffix must not strip, got %q", base)
	}
	// mkdir keeps the base's trailing slash (expansion semantics unchanged).
	if base, ok := inlineFsPathPrefix("lib/ :mkdir sub"); !ok || base != "lib/" {
		t.Fatalf("mkdir prefix = %q, %v", base, ok)
	}
	if base, ok := inlineFsPathPrefix("lib/util.ts :rm"); !ok || base != "lib/util.ts" {
		t.Fatalf("rm prefix = %q, %v", base, ok)
	}
	// Leading-colon form has no path prefix.
	if _, ok := inlineFsPathPrefix(":touch x"); ok {
		t.Fatal("leading-colon form must not report a prefix")
	}
}

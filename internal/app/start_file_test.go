package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestWithStartFileOpensAHiddenFileAtALine(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, ".hidden.env"), []byte("A=1\nB=2\nC=3\n"), 0o644))
	m = m.WithStartFile(".hidden.env", 2)
	if m.mode != modeEdit || m.openRel != ".hidden.env" || m.edit.cy != 1 {
		t.Fatalf("mode=%v open=%q cy=%d err=%q", m.mode, m.openRel, m.edit.cy, m.errText)
	}
}

func TestWithStartFileOpensANewFileCreatedOnSave(t *testing.T) {
	m, root := newTestModel(t, nil)
	target := filepath.Join(root, "new.txt")
	m = m.WithStartFile("new.txt", 0)
	if m.mode != modeEdit || m.openRel != "new.txt" || m.edit.content() != "" || !strings.HasPrefix(m.notice, "new file") {
		t.Fatalf("mode=%v open=%q content=%q notice=%q err=%q", m.mode, m.openRel, m.edit.content(), m.notice, m.errText)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("a new file is not created before the first save")
	}
	m = runes(m, "hello")
	m = key(m, ctrlKey('s'))
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "hello" {
		t.Fatalf("saved %q, %v", data, err)
	}
}

func TestUnsavedNewFileTabKeepsItsDraft(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.WithStartFile("draft.txt", 0)
	m = runes(m, "work in progress")
	m = m.openFileAt("main.go") // switching away stashes the draft
	if m.openRel != "main.go" {
		t.Fatalf("open = %q", m.openRel)
	}
	for i, rel := range m.tabs {
		if rel == "draft.txt" {
			m = m.activateTab(i)
		}
	}
	if m.openRel != "draft.txt" || m.edit.content() != "work in progress" || !m.edit.dirty {
		t.Fatalf("reopened %q with %q (dirty=%v) err=%q", m.openRel, m.edit.content(), m.edit.dirty, m.errText)
	}
	m = key(m, keyPress(tea.KeyEscape))
	m = m.openFileAt("main.go")
	for i, rel := range m.tabs {
		if rel == "draft.txt" {
			m = m.activateTab(i)
		}
	}
	if m.openRel == "draft.txt" {
		t.Fatal("a missing file without a draft is still dropped as gone")
	}
}

func TestOpenNewFileStaysInTheJail(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = m.openNewFileAt("../escape.txt")
	if m.openRel == "../escape.txt" || m.errText == "" {
		t.Fatalf("an escaping new file must be refused: open=%q err=%q", m.openRel, m.errText)
	}
}

func TestOnlyFileModeShowsJustThatFile(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, ".env"), []byte("A=1\n"), 0o644))
	m = m.WithOnlyFile(".env").WithStartFile(".env", 0)

	if paths := treePaths(m); len(paths) != 1 || paths[0] != ".env" {
		t.Fatalf("the tree shows only the file, got %v", paths)
	}
	m = key(m, keyPress(tea.KeyEscape)) // back to the query view
	m = key(m, ctrlKey('p'))
	if got := fuzzyTexts(m); len(got) != 1 || got[0] != ".env" {
		t.Fatalf("Ctrl+P lists only the file, got %v", got)
	}
	m = key(m, keyPress(tea.KeyEscape))

	// The index stays the one file across a rebuild — the folder is never walked.
	m, _ = deliver(m, m.rebuildCorpusCmd()())
	if len(m.corpus) != 1 || m.corpus[0] != ".env" {
		t.Fatalf("rebuilt corpus = %v", m.corpus)
	}

	next := key(m, ctrlKey('w'))
	if next.fuzzyOpen || !strings.Contains(next.errText, "single-file mode") {
		t.Fatalf("Ctrl+W is refused: open=%v err=%q", next.fuzzyOpen, next.errText)
	}
}

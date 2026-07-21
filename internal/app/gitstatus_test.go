package app

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGitStatusMsgSwapsDirtySet(t *testing.T) {
	m, _ := newTestModel(t, nil)

	next, _ := m.Update(gitStatusMsg{dirty: map[string]bool{"main.go": true}, ok: true})
	m = next.(Model)
	if !m.gitDirty["main.go"] {
		t.Fatal("gitStatusMsg must swap the dirty set in")
	}

	// The sidebar entries pick the flag up.
	found := false
	for _, e := range m.treeEntries() {
		if e.RelativePath == "main.go" {
			found = true
			if !e.Uncommitted {
				t.Fatal("main.go must be flagged Uncommitted")
			}
		} else if e.Uncommitted {
			t.Fatalf("clean entry %q must not be flagged", e.RelativePath)
		}
	}
	if !found {
		t.Fatal("main.go missing from tree")
	}

	// A failed refresh (ok=false) must keep the last known set.
	next, _ = m.Update(gitStatusMsg{ok: false})
	m = next.(Model)
	if !m.gitDirty["main.go"] {
		t.Fatal("failed refresh must not clear the dirty set")
	}
}

func TestGitStatusTickReschedules(t *testing.T) {
	m, _ := newTestModel(t, nil)

	// Idle tick: fires a refresh and re-arms.
	next, cmd := m.Update(gitStatusTickMsg{})
	m = next.(Model)
	if !m.gitStatusRunning {
		t.Fatal("tick must mark a refresh in flight")
	}
	if cmd == nil {
		t.Fatal("tick must return a batch (refresh + next tick)")
	}

	// Tick while a refresh is in flight: skips the refresh but keeps the loop.
	next, cmd = m.Update(gitStatusTickMsg{})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("busy tick must still re-arm the loop")
	}

	// The refresh landing clears the in-flight flag.
	next, _ = m.Update(gitStatusMsg{dirty: map[string]bool{}, ok: true})
	m = next.(Model)
	if m.gitStatusRunning {
		t.Fatal("gitStatusMsg must clear the running flag")
	}
}

// uncommittedFixture seeds a model where lib/util.ts is dirty and main.go is
// clean, with the git-repo gate on.
func uncommittedFixture(t *testing.T) Model {
	t.Helper()
	m, _ := newTestModel(t, nil)
	m.gitRepo = true
	dirty := map[string]bool{"lib/util.ts": true, "lib": true}
	next, _ := m.Update(gitStatusMsg{dirty: dirty, ok: true})
	return next.(Model)
}

func TestCtrlUListsOnlyUncommitted(t *testing.T) {
	m := uncommittedFixture(t)
	m = ctrl(m, tea.KeyCtrlU)
	if !m.fuzzyOpen {
		t.Fatal("Ctrl+U must open the fuzzy overlay")
	}
	if m.fuzzyPrompt != "uncommitted " {
		t.Fatalf("fuzzyPrompt = %q, want %q", m.fuzzyPrompt, "uncommitted ")
	}
	if len(m.fuzzyMatches) != 1 {
		t.Fatalf("want exactly the dirty file listed, got %d matches", len(m.fuzzyMatches))
	}
	if rel := m.fuzzyCorpus[m.fuzzyMatches[0].Index].Text; rel != "lib/util.ts" {
		t.Fatalf("listed %q, want lib/util.ts", rel)
	}

	// Filtering works like Ctrl+P; Enter opens the file in edit mode.
	m = runes(m, "util")
	if len(m.fuzzyMatches) != 1 {
		t.Fatalf("filter should keep the match, got %d", len(m.fuzzyMatches))
	}
	m = ctrl(m, tea.KeyEnter)
	if m.fuzzyOpen {
		t.Fatal("Enter must close the overlay")
	}
	if m.openRel != "lib/util.ts" || m.mode != modeEdit {
		t.Fatalf("openRel=%q mode=%v, want lib/util.ts in edit mode", m.openRel, m.mode)
	}
}

func TestCtrlPStillListsEverything(t *testing.T) {
	m := uncommittedFixture(t)
	m = ctrl(m, tea.KeyCtrlP)
	if !m.fuzzyOpen || m.fuzzyPrompt != "goto " {
		t.Fatalf("Ctrl+P overlay: open=%v prompt=%q", m.fuzzyOpen, m.fuzzyPrompt)
	}
	if len(m.fuzzyMatches) < 2 {
		t.Fatalf("Ctrl+P must list the whole corpus, got %d", len(m.fuzzyMatches))
	}
}

func TestCtrlUToggleCloses(t *testing.T) {
	m := uncommittedFixture(t)
	m = ctrl(m, tea.KeyCtrlU)
	if !m.fuzzyOpen {
		t.Fatal("expected overlay open")
	}
	m = ctrl(m, tea.KeyCtrlU)
	if m.fuzzyOpen {
		t.Fatal("Ctrl+U while open must close the overlay")
	}
}

func TestCtrlUNotARepo(t *testing.T) {
	m, _ := newTestModel(t, nil) // temp dir: gitRepo stays false
	m = ctrl(m, tea.KeyCtrlU)
	if m.fuzzyOpen {
		t.Fatal("non-repo must not open the overlay")
	}
	if m.errText != "not a git repository" {
		t.Fatalf("errText = %q", m.errText)
	}
}

func TestCtrlUNothingDirty(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.gitRepo = true // repo, but the dirty set is empty
	m = ctrl(m, tea.KeyCtrlU)
	if m.fuzzyOpen {
		t.Fatal("empty dirty set must not open the overlay")
	}
	if m.notice != "no uncommitted files" {
		t.Fatalf("notice = %q", m.notice)
	}
}

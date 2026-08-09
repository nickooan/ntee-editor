package app

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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

	// A failed refresh (ok=false) must keep the last known set and surface a
	// notice — once, not on every subsequent failing tick.
	next, _ = m.Update(gitStatusMsg{ok: false})
	m = next.(Model)
	if !m.gitDirty["main.go"] {
		t.Fatal("failed refresh must not clear the dirty set")
	}
	if m.notice == "" {
		t.Fatal("first failure must surface a notice")
	}
	m.notice = ""
	next, _ = m.Update(gitStatusMsg{ok: false})
	m = next.(Model)
	if m.notice != "" {
		t.Fatalf("repeat failure must stay quiet, got %q", m.notice)
	}
	// Recovery clears the latch: a later failure notices again.
	next, _ = m.Update(gitStatusMsg{ok: true, dirty: m.gitDirty})
	m = next.(Model)
	next, _ = m.Update(gitStatusMsg{ok: false})
	m = next.(Model)
	if m.notice == "" {
		t.Fatal("failure after recovery must notice again")
	}
}

func TestGitStatusTickReschedules(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.gitRepo = true // the tick only ever runs in a repo (Init gates it)

	// Active tick: fires a refresh and re-arms.
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

func TestGitStatusPollPausesWhenIdleOrBlurred(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.gitRepo = true

	// Idle for longer than the threshold: the tick re-arms without spawning.
	m.lastInputAt = time.Now().Add(-gitIdleThreshold - time.Second)
	next, cmd := m.Update(gitStatusTickMsg{})
	m = next.(Model)
	if m.gitStatusRunning {
		t.Fatal("idle tick must not spawn a refresh")
	}
	if cmd == nil {
		t.Fatal("idle tick must still re-arm the loop")
	}

	// Input wakes the loop up: the next tick refreshes again.
	next, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m = next.(Model)
	next, _ = m.Update(gitStatusTickMsg{})
	m = next.(Model)
	if !m.gitStatusRunning {
		t.Fatal("tick after input must refresh")
	}
	next, _ = m.Update(gitStatusMsg{ok: true, dirty: map[string]bool{}})
	m = next.(Model)

	// Blurred terminal: paused regardless of recent input.
	next, _ = m.Update(tea.BlurMsg{})
	m = next.(Model)
	next, _ = m.Update(gitStatusTickMsg{})
	m = next.(Model)
	if m.gitStatusRunning {
		t.Fatal("blurred tick must not spawn a refresh")
	}

	// Refocus refreshes immediately (external changes may have landed).
	next, cmd = m.Update(tea.FocusMsg{})
	m = next.(Model)
	if !m.gitStatusRunning || cmd == nil {
		t.Fatal("focus must trigger an immediate refresh")
	}
}

func TestManualRefreshRespectsInFlight(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m.gitRepo = true
	m.gitStatusRunning = true
	if _, cmd := m.maybeGitRefresh(); cmd != nil {
		t.Fatal("a refresh in flight must suppress a second spawn")
	}
	m.gitStatusRunning = false
	m2, cmd := m.maybeGitRefresh()
	if cmd == nil || !m2.gitStatusRunning {
		t.Fatal("an idle refresh must spawn and mark in flight")
	}
	m.gitRepo = false
	if _, cmd := m.maybeGitRefresh(); cmd != nil {
		t.Fatal("no repo → no spawn")
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
	m = key(m, ctrlKey('u'))
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
	m = key(m, keyPress(tea.KeyEnter))
	if m.fuzzyOpen {
		t.Fatal("Enter must close the overlay")
	}
	if m.openRel != "lib/util.ts" || m.mode != modeEdit {
		t.Fatalf("openRel=%q mode=%v, want lib/util.ts in edit mode", m.openRel, m.mode)
	}
}

func TestCtrlPStillListsEverything(t *testing.T) {
	m := uncommittedFixture(t)
	m = key(m, ctrlKey('p'))
	if !m.fuzzyOpen || m.fuzzyPrompt != "goto " {
		t.Fatalf("Ctrl+P overlay: open=%v prompt=%q", m.fuzzyOpen, m.fuzzyPrompt)
	}
	if len(m.fuzzyMatches) < 2 {
		t.Fatalf("Ctrl+P must list the whole corpus, got %d", len(m.fuzzyMatches))
	}
}

func TestCtrlUToggleCloses(t *testing.T) {
	m := uncommittedFixture(t)
	m = key(m, ctrlKey('u'))
	if !m.fuzzyOpen {
		t.Fatal("expected overlay open")
	}
	m = key(m, ctrlKey('u'))
	if m.fuzzyOpen {
		t.Fatal("Ctrl+U while open must close the overlay")
	}
}

func TestCtrlUNotARepo(t *testing.T) {
	m, _ := newTestModel(t, nil) // temp dir: gitRepo stays false
	m = key(m, ctrlKey('u'))
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
	m = key(m, ctrlKey('u'))
	if m.fuzzyOpen {
		t.Fatal("empty dirty set must not open the overlay")
	}
	if m.notice != "no uncommitted files" {
		t.Fatalf("notice = %q", m.notice)
	}
}

package app

import "testing"

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

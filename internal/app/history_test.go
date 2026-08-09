package app

import (
	"testing"

	"github.com/nickooan/ntee-editor/internal/store"
)

// countingBackend wraps a Backend and counts SnapshotGet calls — the burst
// dedupe must be hash-only (zero store reads).
type countingBackend struct {
	store.Backend
	gets int
}

func (c *countingBackend) SnapshotGet(seq int64) (store.Snapshot, bool) {
	c.gets++
	return c.Backend.SnapshotGet(seq)
}

func TestPushSnapshotDedupeReadsNothing(t *testing.T) {
	cb := &countingBackend{Backend: store.NewMemory()}
	m, _ := newTestModel(t, cb)
	m = m.openFileAt("main.go")

	cb.gets = 0
	// Identical content: repeated burst flushes must neither read the store
	// nor grow the timeline.
	before := len(m.undoSeqs)
	for i := 0; i < 5; i++ {
		m.snapDirty = true
		m = m.flushBurst()
	}
	if cb.gets != 0 {
		t.Fatalf("dedupe read the store %d times, want 0", cb.gets)
	}
	if len(m.undoSeqs) != before {
		t.Fatalf("no-op flushes grew the timeline: %d → %d", before, len(m.undoSeqs))
	}

	// A save on identical content upgrades the kind without a read.
	m = m.pushSnapshot("save")
	if cb.gets != 0 {
		t.Fatalf("kind upgrade read the store %d times, want 0", cb.gets)
	}
	if snap, ok := cb.SnapshotGet(m.undoSeqs[m.undoCursor]); !ok || snap.Kind != "save" {
		t.Fatalf("kind upgrade did not persist: %+v", snap)
	}
	cb.gets = 0 // that check was the test's own read

	// Real edits still append.
	m.edit.insert("x")
	m.snapDirty = true
	m = m.flushBurst()
	if len(m.undoSeqs) != before+1 {
		t.Fatalf("real edit did not snapshot: %d", len(m.undoSeqs))
	}
	if cb.gets != 0 {
		t.Fatalf("appending read the store %d times, want 0", cb.gets)
	}
}

func TestSnapshotTrimDeletesRecords(t *testing.T) {
	db := store.NewMemory()
	m, _ := newTestModel(t, db)
	m.cfg.Editor.MaxSnapshots = 3
	m = m.openFileAt("main.go")

	var all []int64
	for i := 0; i < 6; i++ {
		m.edit.insert(string(rune('a' + i)))
		m.snapDirty = true
		m = m.flushBurst()
		all = append(all, m.undoSeqs[m.undoCursor])
	}
	if len(m.undoSeqs) != 3 {
		t.Fatalf("timeline = %d, want trimmed to 3", len(m.undoSeqs))
	}
	kept := map[int64]bool{}
	for _, s := range m.undoSeqs {
		kept[s] = true
	}
	for _, s := range all {
		_, ok := db.SnapshotGet(s)
		if kept[s] && !ok {
			t.Fatalf("kept seq %d missing from store", s)
		}
		if !kept[s] && ok {
			t.Fatalf("trimmed seq %d leaked in store", s)
		}
	}
}

func TestContentHashedMemoizesPerRev(t *testing.T) {
	e := newEditor("a\nb")
	c1, h1 := e.contentHashed()
	if c1 != "a\nb" || h1 != store.ContentHash("a\nb") {
		t.Fatalf("contentHashed = %q, %q", c1, h1)
	}
	// Same rev: cached values (identical strings).
	c2, h2 := e.contentHashed()
	if c2 != c1 || h2 != h1 {
		t.Fatal("same-rev call must return the cached values")
	}
	// Mutation bumps rev → recompute.
	e.insert("x")
	c3, h3 := e.contentHashed()
	if c3 == c1 || h3 == h1 {
		t.Fatal("post-mutation call must recompute")
	}
	if c3 != e.content() || h3 != store.ContentHash(e.content()) {
		t.Fatalf("recomputed values wrong: %q %q", c3, h3)
	}
}

// The stash reads only the kept window of the timeline; a long editing
// session must still produce the same trailing draftMaxSteps steps.
func TestStashDraftWindowMatchesOldLogic(t *testing.T) {
	db := store.NewMemory()
	m, _ := newTestModel(t, db)
	m = m.openFileAt("main.go")

	for i := 0; i < draftMaxSteps+5; i++ {
		m.edit.insert(string(rune('a' + i%26)))
		m.snapDirty = true
		m = m.flushBurst()
	}
	m = m.stashDraftIfDirty()

	d, ok := db.LoadDraft(m.openRel)
	if !ok {
		t.Fatal("no draft stashed")
	}
	if len(d.Steps) != draftMaxSteps {
		t.Fatalf("steps = %d, want %d", len(d.Steps), draftMaxSteps)
	}
	// The trailing step is the live buffer, and steps replay the newest
	// snapshots in order (old logic: read everything, keep the last 15).
	if d.Steps[len(d.Steps)-1].Content != m.edit.content() {
		t.Fatal("last step must be the live buffer")
	}
	want := m.undoSeqs[m.undoCursor+1-draftMaxSteps : m.undoCursor+1]
	for i, seq := range want {
		snap, _ := db.SnapshotGet(seq)
		if d.Steps[i].Content != snap.Content {
			t.Fatalf("step %d does not match snapshot %d", i, seq)
		}
	}
}

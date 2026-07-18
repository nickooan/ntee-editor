package store

import (
	"os"
	"path/filepath"
	"testing"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

// openTestStore opens a Store on a temp dir, bypassing the ~/.ntee-editor
// hashing so tests stay hermetic.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "db")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := nteedb.Open(nteedb.Options{
		Dir: dir,
		Indexes: []nteedb.IndexDef{
			{Name: "file", Kind: nteedb.KindString, MaxPerValue: 50},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSnapshotRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if err := s.SnapshotPut("a.go", 100, "edit", "hello"); err != nil {
		t.Fatal(err)
	}
	snap, ok := s.SnapshotGet(100)
	if !ok || snap.Content != "hello" || snap.Kind != "edit" || snap.Path != "a.go" {
		t.Fatalf("round trip failed: %+v ok=%v", snap, ok)
	}
	s.SnapshotDelete([]int64{100})
	if _, ok := s.SnapshotGet(100); ok {
		t.Fatal("snapshot survived delete")
	}
}

func TestLastSave(t *testing.T) {
	s := openTestStore(t)
	_ = s.SnapshotPut("a.go", 1, "edit", "v1")
	_ = s.SnapshotPut("a.go", 2, "save", "saved-1")
	_ = s.SnapshotPut("a.go", 3, "edit", "v3")
	_ = s.SnapshotPut("a.go", 4, "save", "saved-2")
	_ = s.SnapshotPut("a.go", 5, "edit", "v5")
	_ = s.SnapshotPut("b.go", 6, "save", "other-file")

	snap, ok := s.LastSave("a.go")
	if !ok || snap.Content != "saved-2" {
		t.Fatalf("want newest save of a.go, got %+v ok=%v", snap, ok)
	}
	if _, ok := s.LastSave("missing.go"); ok {
		t.Fatal("LastSave hit for unknown path")
	}
}

func TestRecentFilesOrderAndRePut(t *testing.T) {
	s := openTestStore(t)
	_ = s.TouchOpened(OpenedFile{Path: "old.go", LastOpenedAt: 100})
	_ = s.TouchOpened(OpenedFile{Path: "mid.go", LastOpenedAt: 200})
	_ = s.TouchOpened(OpenedFile{Path: "new.go", LastOpenedAt: 300})
	// Re-open the oldest — it must move to the front.
	_ = s.TouchOpened(OpenedFile{Path: "old.go", LastOpenedAt: 400})

	got := s.RecentFiles(2)
	if len(got) != 2 || got[0].Path != "old.go" || got[1].Path != "new.go" {
		t.Fatalf("recents wrong: %+v", got)
	}
}

func TestSessionRoundTrip(t *testing.T) {
	s := openTestStore(t)
	if _, ok := s.LoadSession(); ok {
		t.Fatal("session should start empty")
	}
	want := Session{LastFile: "a.go", Expanded: []string{"internal", "internal/app"}, TreeIndex: 3}
	if err := s.SaveSession(want); err != nil {
		t.Fatal(err)
	}
	got, ok := s.LoadSession()
	if !ok || got.LastFile != want.LastFile || got.TreeIndex != 3 || len(got.Expanded) != 2 {
		t.Fatalf("session round trip failed: %+v ok=%v", got, ok)
	}
}

func TestMemoryBackendParity(t *testing.T) {
	var b Backend = NewMemory()
	_ = b.SnapshotPut("a.go", 1, "save", "one")
	_ = b.SnapshotPut("a.go", 2, "edit", "two")
	if snap, ok := b.LastSave("a.go"); !ok || snap.Content != "one" {
		t.Fatalf("memory LastSave: %+v ok=%v", snap, ok)
	}
	if snap, ok := b.SnapshotGet(2); !ok || snap.Content != "two" {
		t.Fatalf("memory SnapshotGet: %+v ok=%v", snap, ok)
	}
	_ = b.TouchOpened(OpenedFile{Path: "a.go", LastOpenedAt: 2})
	_ = b.TouchOpened(OpenedFile{Path: "b.go", LastOpenedAt: 1})
	recents := b.RecentFiles(10)
	if len(recents) != 2 || recents[0].Path != "a.go" {
		t.Fatalf("memory recents: %+v", recents)
	}
}

package store

import (
	"errors"
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

// DeleteOpenedUnder prunes rel and its subtree, honoring path boundaries:
// removing "lib" must not touch "library/…". Run against both backends.
func TestDeleteOpenedUnder(t *testing.T) {
	check := func(t *testing.T, s Backend) {
		t.Helper()
		for _, p := range []string{"lib/util.ts", "lib/deep/x.go", "library/y.go", "main.go"} {
			_ = s.TouchOpened(OpenedFile{Path: p, LastOpenedAt: 100})
		}
		if err := s.DeleteOpenedUnder("lib"); err != nil {
			t.Fatal(err)
		}
		left := map[string]bool{}
		for _, f := range s.RecentFiles(0) {
			left[f.Path] = true
		}
		if left["lib/util.ts"] || left["lib/deep/x.go"] {
			t.Fatalf("lib subtree must be pruned: %v", left)
		}
		if !left["library/y.go"] || !left["main.go"] {
			t.Fatalf("boundary sibling and unrelated file must survive: %v", left)
		}

		// Removing a single file prunes exactly that record.
		if err := s.DeleteOpenedUnder("main.go"); err != nil {
			t.Fatal(err)
		}
		for _, f := range s.RecentFiles(0) {
			if f.Path == "main.go" {
				t.Fatal("main.go record must be gone")
			}
		}
	}
	t.Run("store", func(t *testing.T) { check(t, openTestStore(t)) })
	t.Run("memory", func(t *testing.T) { check(t, NewMemory()) })
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
	exerciseDraftsAndTabs(t, b)
}

// exerciseDraftsAndTabs runs the draft/tab contract against any Backend.
func exerciseDraftsAndTabs(t *testing.T, b Backend) {
	t.Helper()
	if _, ok := b.LoadDraft("a.go"); ok {
		t.Fatal("draft should start absent")
	}
	d := Draft{
		Path: "a.go", Content: "unsaved", Cx: 3, Cy: 1, ScrollY: 2,
		Steps: []DraftStep{{Kind: "edit", Content: "un"}, {Kind: "edit", Content: "unsaved"}},
		At:    42,
	}
	if err := b.SaveDraft(d); err != nil {
		t.Fatal(err)
	}
	got, ok := b.LoadDraft("a.go")
	if !ok || got.Content != "unsaved" || got.Cx != 3 || got.Cy != 1 || len(got.Steps) != 2 ||
		got.Steps[1].Content != "unsaved" {
		t.Fatalf("draft round trip: %+v ok=%v", got, ok)
	}
	if err := b.DeleteDraft("a.go"); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.LoadDraft("a.go"); ok {
		t.Fatal("draft survived delete")
	}

	if _, ok := b.LoadTabs(); ok {
		t.Fatal("tabs should start absent")
	}
	if err := b.SaveTabs(Tabs{
		Paths:   []string{"a.go", "lib/b.ts"},
		Active:  1,
		Cursors: map[string]TabCursor{"a.go": {Cy: 40, Cx: 3}},
	}); err != nil {
		t.Fatal(err)
	}
	tabs, ok := b.LoadTabs()
	if !ok || len(tabs.Paths) != 2 || tabs.Active != 1 || tabs.Paths[1] != "lib/b.ts" {
		t.Fatalf("tabs round trip: %+v ok=%v", tabs, ok)
	}
	if c := tabs.Cursors["a.go"]; c.Cy != 40 || c.Cx != 3 {
		t.Fatalf("tab cursors round trip: %+v", tabs.Cursors)
	}
	// Overwrite wins.
	_ = b.SaveTabs(Tabs{Paths: []string{"a.go"}, Active: 0})
	tabs, _ = b.LoadTabs()
	if len(tabs.Paths) != 1 || tabs.Active != 0 {
		t.Fatalf("tabs overwrite: %+v", tabs)
	}
}

func TestStoreDraftsAndTabs(t *testing.T) {
	exerciseDraftsAndTabs(t, openTestStore(t))
}

// TestDraftSurvivesSnapshotEviction proves drafts use plain keys: flooding a
// file's versions past MaxPerValue must never evict its draft.
func TestDraftSurvivesSnapshotEviction(t *testing.T) {
	s := openTestStore(t) // MaxPerValue: 50
	_ = s.SaveDraft(Draft{Path: "a.go", Content: "precious"})
	for i := int64(1); i <= 60; i++ {
		_ = s.SnapshotPut("a.go", i, "edit", "v")
	}
	if d, ok := s.LoadDraft("a.go"); !ok || d.Content != "precious" {
		t.Fatalf("draft lost to snapshot eviction: %+v ok=%v", d, ok)
	}
}

func TestMaintenanceAndCompact(t *testing.T) {
	s := openTestStore(t)
	// Overwrite the same key repeatedly so the main log holds dead versions.
	for i := 0; i < 20; i++ {
		if err := s.SaveSession(Session{Command: "cmd"}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := s.Maintenance()
	if err != nil {
		t.Fatal(err)
	}
	if info.Records == 0 {
		t.Fatal("expected live records")
	}
	if info.MainBytes <= info.LiveBytes {
		t.Fatalf("expected dead space before compact: main=%d live=%d", info.MainBytes, info.LiveBytes)
	}
	if err := s.Compact(); err != nil {
		t.Fatal(err)
	}
	after, err := s.Maintenance()
	if err != nil {
		t.Fatal(err)
	}
	if after.MainBytes != after.LiveBytes {
		t.Fatalf("compact should drop dead space: main=%d live=%d", after.MainBytes, after.LiveBytes)
	}
	if err := s.RelieveBlobs(); err != nil {
		t.Fatalf("relieve on a healthy store should succeed: %v", err)
	}
}

func TestMemoryMaintenanceUnavailable(t *testing.T) {
	m := NewMemory()
	if _, err := m.Maintenance(); !errors.Is(err, ErrNoStats) {
		t.Fatalf("Maintenance err = %v, want ErrNoStats", err)
	}
	if err := m.Compact(); !errors.Is(err, ErrNoStats) {
		t.Fatalf("Compact err = %v, want ErrNoStats", err)
	}
	if err := m.RelieveBlobs(); !errors.Is(err, ErrNoStats) {
		t.Fatalf("RelieveBlobs err = %v, want ErrNoStats", err)
	}
}

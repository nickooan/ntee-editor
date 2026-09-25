package store

import "testing"

// exerciseScoped runs the Scoped view's contract against any inner Backend.
func exerciseScoped(t *testing.T, inner Backend) {
	t.Helper()
	repo := Scoped(inner, "libs/core")

	// File records go in prefixed and come out repo-relative.
	_ = repo.TouchOpened(OpenedFile{Path: "lib.go", LastOpenedAt: 3})
	_ = inner.TouchOpened(OpenedFile{Path: "apps/web/main.go", LastOpenedAt: 2})
	_ = inner.TouchOpened(OpenedFile{Path: "libs/core/old.go", LastOpenedAt: 1})
	recents := repo.RecentFiles(10)
	if len(recents) != 2 || recents[0].Path != "lib.go" || recents[1].Path != "old.go" {
		t.Fatalf("scoped recents: %+v", recents)
	}
	if got := repo.RecentFiles(1); len(got) != 1 || got[0].Path != "lib.go" {
		t.Fatalf("limit applies after filtering: %+v", got)
	}
	if all := inner.RecentFiles(10); all[0].Path != "libs/core/lib.go" {
		t.Fatalf("inner must see the workspace path: %+v", all)
	}

	_ = repo.SnapshotPut("lib.go", 10, "save", "v1")
	if snap, ok := inner.SnapshotGet(10); !ok || snap.Path != "libs/core/lib.go" {
		t.Fatalf("inner snapshot path: %+v ok=%v", snap, ok)
	}
	if snap, ok := repo.SnapshotGet(10); !ok || snap.Path != "lib.go" {
		t.Fatalf("scoped snapshot path: %+v ok=%v", snap, ok)
	}
	if snap, ok := repo.LastSave("lib.go"); !ok || snap.Content != "v1" || snap.Path != "lib.go" {
		t.Fatalf("scoped LastSave: %+v ok=%v", snap, ok)
	}

	// A draft written from the workspace is visible inside the repo.
	_ = inner.SaveDraft(Draft{Path: "libs/core/lib.go", Content: "unsaved"})
	if d, ok := repo.LoadDraft("lib.go"); !ok || d.Content != "unsaved" || d.Path != "lib.go" {
		t.Fatalf("scoped draft: %+v ok=%v", d, ok)
	}
	_ = repo.DeleteDraft("lib.go")
	if _, ok := inner.LoadDraft("libs/core/lib.go"); ok {
		t.Fatal("scoped delete must remove the workspace record")
	}

	_ = repo.DeleteOpenedUnder("lib.go")
	for _, f := range inner.RecentFiles(10) {
		if f.Path == "libs/core/lib.go" {
			t.Fatal("scoped DeleteOpenedUnder must remove the workspace record")
		}
	}

	// Tabs and the index are per-scope singletons, separate from the project's.
	_ = inner.SaveTabs(Tabs{Paths: []string{"main.go"}})
	_ = repo.SaveTabs(Tabs{Paths: []string{"lib.go"}})
	if tabs, ok := repo.LoadTabs(); !ok || len(tabs.Paths) != 1 || tabs.Paths[0] != "lib.go" {
		t.Fatalf("scoped tabs: %+v ok=%v", tabs, ok)
	}
	if tabs, ok := inner.LoadTabs(); !ok || tabs.Paths[0] != "main.go" {
		t.Fatalf("project tabs overwritten: %+v ok=%v", tabs, ok)
	}
	if _, ok := Scoped(inner, "apps/web").LoadTabs(); ok {
		t.Fatal("another repo must not see these tabs")
	}
	_ = repo.SaveCorpus(CorpusIndex{Version: CorpusVersion, Files: []string{"lib.go"}})
	if idx, ok := repo.LoadCorpus(); !ok || idx.Files[0] != "lib.go" {
		t.Fatalf("scoped corpus: %+v ok=%v", idx, ok)
	}
	if _, ok := inner.LoadCorpus(); ok {
		t.Fatal("the project index must stay separate")
	}

	// The session is workspace-level: the view passes it through.
	_ = repo.SaveSession(Session{WorkspaceRepo: "libs/core"})
	if sess, ok := inner.LoadSession(); !ok || sess.WorkspaceRepo != "libs/core" {
		t.Fatalf("session must pass through: %+v ok=%v", sess, ok)
	}

	if Scoped(inner, "") != inner {
		t.Fatal(`Scoped(inner, "") must return inner`)
	}
}

func TestScopedStore(t *testing.T)  { exerciseScoped(t, openTestStore(t)) }
func TestScopedMemory(t *testing.T) { exerciseScoped(t, NewMemory()) }

func TestSessionRepoPositionsRoundTrip(t *testing.T) {
	s := openTestStore(t)
	want := Session{
		LastFile:      "main.go",
		WorkspaceRepo: "libs/core",
		Repos:         map[string]RepoSession{"libs/core": {LastFile: "lib.go", Command: "nested/"}},
	}
	if err := s.SaveSession(want); err != nil {
		t.Fatal(err)
	}
	got, ok := s.LoadSession()
	if !ok || got.Repos["libs/core"].LastFile != "lib.go" || got.Repos["libs/core"].Command != "nested/" {
		t.Fatalf("repo positions round trip: %+v ok=%v", got, ok)
	}
}

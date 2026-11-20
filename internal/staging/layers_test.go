package staging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nickooan/ntee-editor/internal/store"
)

const diskA = "one\ntwo\nthree\ntwo\n"

func latest(t *testing.T, registry *Registry, rel string) (string, string) {
	t.Helper()
	content, session, exists, err := registry.Latest(rel)
	must(t, err)
	if !exists {
		t.Fatalf("%s does not exist", rel)
	}
	return content, session
}

func snapshotFile(t *testing.T, registry *Registry, session, rel string) store.AgentEditFile {
	t.Helper()
	record, ok := registry.Snapshot(session)
	if !ok {
		t.Fatalf("no pending session %s", session)
	}
	file, ok := fileIn([]store.AgentEditSession{record}, session, rel)
	if !ok {
		t.Fatalf("%s has no %s", session, rel)
	}
	return file
}

func TestLatestIsTheTopStagedLayer(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	if content, session := latest(t, registry, "a.go"); content != diskA || session != "" {
		t.Fatalf("nothing staged: the disk (%q from %q)", content, session)
	}
	_, err := registry.Replace("A", "a.go", Replacement{Old: "one", New: "ONE"})
	must(t, err)
	if content, session := latest(t, registry, "a.go"); session != "A" || !strings.HasPrefix(content, "ONE\n") {
		t.Fatalf("one layer: %q from %q", content, session)
	}

	result, err := registry.Replace("B", "a.go", Replacement{Old: "three", New: "THREE"})
	must(t, err)
	if result.BaseSession != "A" {
		t.Fatalf("B's first edit says what it builds on: %+v", result)
	}
	if file := snapshotFile(t, registry, "B", "a.go"); file.BaseSession != "A" || file.BaseContent != "ONE\ntwo\nthree\ntwo\n" || !file.BaseExists {
		t.Fatalf("B's base is A's staged copy: %+v", file)
	}
	if content, session := latest(t, registry, "a.go"); session != "B" || content != "ONE\ntwo\nTHREE\ntwo\n" {
		t.Fatalf("a chain: the top is B (%q from %q)", content, session)
	}
	if content, from, _, _ := registry.Content("A", "a.go"); from != "A" || content != "ONE\ntwo\nthree\ntwo\n" {
		t.Fatalf("A still reads its own copy: %q from %q", content, from)
	}
	if session, _ := registry.StagedIn("a.go"); session != "B" {
		t.Fatalf("StagedIn = %q", session)
	}
}

func TestADiscardedLayerLeavesItsStackOrphaned(t *testing.T) {
	registry, root, _ := newTestRegistry(t)
	_, err := registry.Replace("A", "a.go", Replacement{Old: "one", New: "ONE"})
	must(t, err)
	_, err = registry.Replace("B", "a.go", Replacement{Old: "three", New: "THREE"})
	must(t, err)
	_, err = registry.Discard("A", "the user", 20)
	must(t, err)
	if content, session := latest(t, registry, "a.go"); content != diskA || session != "" {
		t.Fatalf("B built on discarded edits, so the latest is the disk: %q from %q", content, session)
	}
	record, _ := registry.Snapshot("B")
	results := registry.ApplyFiles("B", record.Files, nil)
	if result := results["a.go"]; result.Result != store.AgentEditFileConflicted || !strings.Contains(result.Note, "edit session A's edits, which were discarded") {
		t.Fatalf("B's file is not written: %+v", result)
	}
	if readFile(t, root, "a.go") != diskA {
		t.Fatal("the disk is untouched")
	}
}

func TestALayerOnAnAppliedSessionAppliesNormally(t *testing.T) {
	registry, root, _ := newTestRegistry(t)
	_, err := registry.Replace("A", "a.go", Replacement{Old: "one", New: "ONE"})
	must(t, err)
	_, err = registry.Replace("B", "a.go", Replacement{Old: "three", New: "THREE"})
	must(t, err)
	recordA, _ := registry.Snapshot("A")
	_, err = registry.Finish("A", store.AgentEditPolicyReview, registry.ApplyFiles("A", recordA.Files, nil), 20)
	must(t, err)
	if content, session := latest(t, registry, "a.go"); session != "B" || content != "ONE\ntwo\nTHREE\ntwo\n" {
		t.Fatalf("A is on disk, so B is the root and the top: %q from %q", content, session)
	}
	recordB, _ := registry.Snapshot("B")
	if below, _, ok := registry.ApplyCheck(recordB.Files[0]); !ok || len(below) != 0 {
		t.Fatalf("nothing below B is pending: %v %v", below, ok)
	}
	results := registry.ApplyFiles("B", recordB.Files, nil)
	if results["a.go"].Result != store.AgentEditFileApplied || readFile(t, root, "a.go") != "ONE\ntwo\nTHREE\ntwo\n" {
		t.Fatalf("B applies on top of A: %+v", results)
	}
}

func TestApplyingAStackedFileWritesAndSettlesTheLayersBelow(t *testing.T) {
	registry, root, log := newTestRegistry(t)
	_, err := registry.Replace("A", "a.go", Replacement{Old: "one", New: "ONE"})
	must(t, err)
	_, err = registry.Write("A", "other.go", "other\n")
	must(t, err)
	_, err = registry.Replace("B", "a.go", Replacement{Old: "three", New: "THREE"})
	must(t, err)

	recordB, _ := registry.Snapshot("B")
	if below, _, ok := registry.ApplyCheck(recordB.Files[0]); !ok || len(below) != 1 || below[0] != "A" {
		t.Fatalf("B's a.go writes A's along: %v %v", below, ok)
	}
	finishedB, err := registry.Finish("B", store.AgentEditPolicyReview, registry.ApplyFiles("B", recordB.Files, nil), 20)
	must(t, err)
	if finishedB.Status != store.AgentEditApplied || readFile(t, root, "a.go") != "ONE\ntwo\nTHREE\ntwo\n" {
		t.Fatalf("B applied both edits: %s %q", finishedB.Status, readFile(t, root, "a.go"))
	}
	settled := snapshotFile(t, registry, "A", "a.go")
	if settled.Result != store.AgentEditFileApplied || !strings.Contains(settled.ResultNote, "written with edit session B") {
		t.Fatalf("A's copy is settled: %+v", settled)
	}
	if pending, _ := log.LoadPendingAgentEdits(); len(pending) != 1 || pending[0].Files[0].Result != store.AgentEditFileApplied {
		t.Fatalf("the settled result is saved: %+v", pending)
	}
	if _, err := registry.Replace("A", "a.go", Replacement{Old: "ONE", New: "1"}); err == nil || !strings.Contains(err.Error(), "already written") {
		t.Fatalf("A cannot edit a file already written: %v", err)
	}
	if content, session := latest(t, registry, "a.go"); session != "" || content != "ONE\ntwo\nTHREE\ntwo\n" {
		t.Fatalf("the settled layer is no layer: %q from %q", content, session)
	}

	recordA, _ := registry.Snapshot("A")
	finishedA, err := registry.Finish("A", store.AgentEditPolicyReview, registry.ApplyFiles("A", recordA.Files, nil), 20)
	must(t, err)
	if finishedA.Status != store.AgentEditApplied || readFile(t, root, "other.go") != "other\n" || readFile(t, root, "a.go") != "ONE\ntwo\nTHREE\ntwo\n" {
		t.Fatalf("A applies the rest and keeps the settled file: %s %s", finishedA.Status, finishedA.Outcome)
	}
}

func TestAStackedFileConflictsWhenTheLayerBelowChanged(t *testing.T) {
	registry, root, _ := newTestRegistry(t)
	_, err := registry.Replace("A", "a.go", Replacement{Old: "one", New: "ONE"})
	must(t, err)
	_, err = registry.Replace("B", "a.go", Replacement{Old: "three", New: "THREE"})
	must(t, err)
	_, err = registry.Replace("A", "a.go", Replacement{Old: "ONE", New: "1"})
	must(t, err)
	recordB, _ := registry.Snapshot("B")
	_, refusal, ok := registry.ApplyCheck(recordB.Files[0])
	if ok || refusal.Result != store.AgentEditFileConflicted || !strings.Contains(refusal.Note, "edit session A changed it after this session built on it") {
		t.Fatalf("refusal = %+v", refusal)
	}
	registry.ApplyFiles("B", recordB.Files, nil)
	if readFile(t, root, "a.go") != diskA || snapshotFile(t, registry, "A", "a.go").Result != "" {
		t.Fatal("nothing is written and A is untouched")
	}

	must(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("someone\n"), 0o644))
	recordA, _ := registry.Snapshot("A")
	if _, refusal, ok := registry.ApplyCheck(recordA.Files[0]); ok || !strings.Contains(refusal.Note, "changed on disk") {
		t.Fatalf("the disk changed under A: %+v", refusal)
	}
}

func TestANewFileStacksToo(t *testing.T) {
	registry, root, _ := newTestRegistry(t)
	_, err := registry.Write("A", "pkg/new.go", "package pkg\n")
	must(t, err)
	_, err = registry.Replace("B", "pkg/new.go", Replacement{Old: "package pkg\n", New: "package pkg\n\nfunc New() {}\n"})
	must(t, err)
	recordB, _ := registry.Snapshot("B")
	results := registry.ApplyFiles("B", recordB.Files, nil)
	if results["pkg/new.go"].Result != store.AgentEditFileApplied || readFile(t, root, "pkg/new.go") != "package pkg\n\nfunc New() {}\n" {
		t.Fatalf("B creates the file with both edits: %+v", results)
	}
}

func TestADecidedWriteSettlesTheLayersItNames(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	_, err := registry.Replace("A", "a.go", Replacement{Old: "one", New: "ONE"})
	must(t, err)
	_, err = registry.Replace("B", "a.go", Replacement{Old: "three", New: "THREE"})
	must(t, err)
	recordB, _ := registry.Snapshot("B")
	decided := map[string]FileResult{"a.go": {Result: store.AgentEditFileApplied, Settles: []string{"A"}}}
	registry.ApplyFiles("B", recordB.Files, decided)
	if settled := snapshotFile(t, registry, "A", "a.go"); settled.Result != store.AgentEditFileApplied {
		t.Fatalf("a write the caller made (the open buffer) settles A too: %+v", settled)
	}
}

func TestConcurrentFirstStagingsNeverFork(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	sessions := []string{"s1", "s2", "s3", "s4", "s5", "s6"}
	var wait sync.WaitGroup
	for _, session := range sessions {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := registry.Write(session, "a.go", diskA+session+"\n")
			must(t, err)
		}()
	}
	wait.Wait()
	bases := map[string]bool{}
	for _, session := range sessions {
		base := snapshotFile(t, registry, session, "a.go").BaseSession
		if bases[base] {
			t.Fatalf("two sessions built on %q", base)
		}
		bases[base] = true
	}
	if !bases[""] {
		t.Fatal("one session is the root, built on the disk")
	}
}

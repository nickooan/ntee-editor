package staging

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickooan/ntee-editor/internal/store"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// newTestRegistry is a registry over a temp workspace holding a.go, with a
// clock that ticks a second per call.
func newTestRegistry(t *testing.T) (*Registry, string, *store.MemoryAgentEditLog) {
	t.Helper()
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("one\ntwo\nthree\ntwo\n"), 0o644))
	log := store.NewMemoryAgentEditLog()
	clock := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex // edits on several goroutines read the clock
	registry := NewRegistry(root, log).WithClock(func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock = clock.Add(time.Second)
		return clock
	})
	return registry, root, log
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	must(t, err)
	return string(data)
}

func stagedContent(t *testing.T, registry *Registry, session, rel string) string {
	t.Helper()
	content, from, _, err := registry.Content(session, rel)
	must(t, err)
	if from != session {
		t.Fatalf("%s is not staged in %s", rel, session)
	}
	return content
}

func TestReplaceNeedsExactlyOneMatch(t *testing.T) {
	registry, root, _ := newTestRegistry(t)
	if _, err := registry.Replace("s1", "", "a.go", Replacement{Old: "missing", New: "x"}); err == nil || !strings.Contains(err.Error(), "not in a.go") {
		t.Fatalf("no match is refused: %v", err)
	}
	if _, ok := registry.Snapshot("s1"); ok {
		t.Fatal("a refused first edit leaves no session behind")
	}
	_, err := registry.Replace("s1", "", "a.go", Replacement{Old: "two", New: "2"})
	if err == nil || !strings.Contains(err.Error(), "matches 2 times in a.go (lines 2, 4)") {
		t.Fatalf("several matches are refused with their lines: %v", err)
	}

	result, err := registry.Replace("s1", "", "a.go", Replacement{Old: "three", New: "3\n3b"})
	must(t, err)
	if result.LineFrom != 3 || result.LineTo != 4 || result.EditCount != 1 || result.StagedFiles != 1 || !result.SessionStart {
		t.Fatalf("result = %+v", result)
	}
	if got := readFile(t, root, "a.go"); got != "one\ntwo\nthree\ntwo\n" {
		t.Fatalf("staging never touches the file: %q", got)
	}
	if got := stagedContent(t, registry, "s1", "a.go"); got != "one\ntwo\n3\n3b\ntwo\n" {
		t.Fatalf("staged content = %q", got)
	}
}

func TestReplaceRefusesEmptyAndSameText(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	for _, replacement := range []Replacement{
		{Old: "", New: "x"},
		{Old: "one", New: "one"},
		{Old: "two", New: "2", All: true, HintLine: 2},
	} {
		if _, err := registry.Replace("s1", "", "a.go", replacement); err == nil {
			t.Fatalf("%+v must be refused", replacement)
		}
	}
}

func TestReplaceHintPicksTheMatch(t *testing.T) {
	registry, root, _ := newTestRegistry(t)
	result, err := registry.Replace("s1", "", "a.go", Replacement{Old: "two", New: "TWO", HintLine: 4})
	must(t, err)
	if result.LineFrom != 4 || stagedContent(t, registry, "s1", "a.go") != "one\ntwo\nthree\nTWO\n" {
		t.Fatalf("the hint takes the match on its line: %+v", result)
	}

	// No match covers line 3: the nearest wins, the earlier on a tie.
	must(t, os.WriteFile(filepath.Join(root, "b.go"), []byte("x\nmid\nx\n\n\n\nx\n"), 0o644))
	result, err = registry.Replace("s2", "", "b.go", Replacement{Old: "x", New: "y", HintLine: 5})
	must(t, err)
	if result.LineFrom != 3 {
		t.Fatalf("line 5 is nearest the match on line 3 (2 away, line 7 too — the earlier wins): %+v", result)
	}
	// A single match ignores the hint.
	result, err = registry.Replace("s2", "", "b.go", Replacement{Old: "mid", New: "MID", HintLine: 40})
	must(t, err)
	if result.LineFrom != 2 {
		t.Fatalf("a single match is taken whatever the hint: %+v", result)
	}
}

func TestReplaceAllAndEditsStack(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	result, err := registry.Replace("s1", "", "a.go", Replacement{Old: "two", New: "2", All: true})
	must(t, err)
	if result.Replaced != 2 || result.LineFrom != 2 || result.LineTo != 4 {
		t.Fatalf("replace-all = %+v", result)
	}
	// The next edit works on the staged content, not the disk.
	result, err = registry.Replace("s1", "", "a.go", Replacement{Old: "2\nthree", New: "2+3"})
	must(t, err)
	if result.EditCount != 2 || result.SessionStart {
		t.Fatalf("second edit = %+v", result)
	}
	if got := stagedContent(t, registry, "s1", "a.go"); got != "one\n2+3\n2\n" {
		t.Fatalf("stacked content = %q", got)
	}
	record, _ := registry.Snapshot("s1")
	if len(record.Files) != 1 || len(record.Files[0].Edits) != 2 || record.Files[0].BaseContent != "one\ntwo\nthree\ntwo\n" {
		t.Fatalf("one file, two edits, base kept: %+v", record.Files)
	}
}

func TestWriteStagesANewFile(t *testing.T) {
	registry, root, _ := newTestRegistry(t)
	result, err := registry.Write("s1", "", "pkg/new.go", "package pkg\n\nfunc New() {}\n")
	must(t, err)
	if !result.NewFile || result.Added != 3 || result.Removed != 0 || result.LineFrom != 1 || result.LineTo != 3 {
		t.Fatalf("new file = %+v", result)
	}
	result, err = registry.Write("s1", "", "a.go", "one\nTWO\nthree\ntwo\n")
	must(t, err)
	if result.NewFile || result.Added != 1 || result.Removed != 1 || result.LineFrom != 2 || result.LineTo != 2 || result.StagedFiles != 2 {
		t.Fatalf("rewrite = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "pkg")); !os.IsNotExist(err) {
		t.Fatal("staging creates nothing on disk")
	}
	if content, from, exists, _ := registry.Content("other", "pkg/new.go"); !exists || from != "s1" || content != "package pkg\n\nfunc New() {}\n" {
		t.Fatalf("another session sees the latest content, s1's staged new file: %q from %q", content, from)
	}
}

func TestStagingRefusesBinaryAndOutsideFiles(t *testing.T) {
	registry, root, _ := newTestRegistry(t)
	must(t, os.WriteFile(filepath.Join(root, "blob.bin"), []byte("a\x00b"), 0o644))
	if _, err := registry.Write("s1", "", "blob.bin", "text"); err == nil || !strings.Contains(err.Error(), "binary") {
		t.Fatalf("binary files are refused: %v", err)
	}
	if _, err := registry.Write("s1", "", "../outside.go", "x"); err == nil {
		t.Fatal("a path outside the workspace is refused")
	}
}

func TestApplyWritesAndSkipsConflicts(t *testing.T) {
	registry, root, log := newTestRegistry(t)
	must(t, os.WriteFile(filepath.Join(root, "c.go"), []byte("c\n"), 0o644))
	_, err := registry.Replace("s1", "", "a.go", Replacement{Old: "one", New: "ONE"})
	must(t, err)
	_, err = registry.Write("s1", "", "c.go", "C\n")
	must(t, err)
	_, err = registry.Write("s1", "", "dir/new.go", "new\n")
	must(t, err)
	// Someone changes c.go after it was staged: applying must not lose it.
	must(t, os.WriteFile(filepath.Join(root, "c.go"), []byte("someone else\n"), 0o644))

	record, _ := registry.Snapshot("s1")
	results := registry.ApplyFiles("s1", record.Files, nil)
	if results["a.go"].Result != store.AgentEditFileApplied || results["dir/new.go"].Result != store.AgentEditFileApplied {
		t.Fatalf("results = %+v", results)
	}
	if results["c.go"].Result != store.AgentEditFileConflicted {
		t.Fatalf("c.go changed on disk: %+v", results["c.go"])
	}
	if readFile(t, root, "a.go") != "ONE\ntwo\nthree\ntwo\n" || readFile(t, root, "dir/new.go") != "new\n" || readFile(t, root, "c.go") != "someone else\n" {
		t.Fatal("applied files are written, the conflicted one is left alone")
	}

	finished, err := registry.Finish("s1", store.AgentEditPolicyApply, store.GitFollowUpNone, results, 20)
	must(t, err)
	if finished.Status != store.AgentEditPartlyApplied || !strings.Contains(finished.Outcome, "applied 2 of 3 files") || !strings.Contains(finished.Outcome, "c.go (changed on disk") {
		t.Fatalf("finished = %s: %s", finished.Status, finished.Outcome)
	}
	if _, ok := registry.Snapshot("s1"); ok {
		t.Fatal("a finished session is no longer pending")
	}
	if summary, ok := registry.Finished("s1"); !ok || summary.Files[0].Content != "" {
		t.Fatal("this run remembers the finished session, without its contents")
	}
	if _, err := registry.Write("s1", "", "a.go", "again"); err == nil || !strings.Contains(err.Error(), "already finished") {
		t.Fatalf("a finished id does not take more edits: %v", err)
	}

	// The record is kept whole for tracing.
	sessions, _ := log.LoadFinishedAgentEdits()
	if len(sessions) != 1 || len(sessions[0].Files) != 3 || sessions[0].Files[0].Content == "" || sessions[0].Files[0].Result == "" {
		t.Fatalf("finished records = %+v", sessions)
	}
	pending, _ := log.LoadPendingAgentEdits()
	if len(pending) != 0 {
		t.Fatal("the pending record is gone once finished")
	}
}

func TestEditsAfterEndReopenUntilTheUserReviews(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	_, err := registry.Write("s1", "", "a.go", "x\n")
	must(t, err)
	record, allEnded, err := registry.MarkAgentEnded("s1", "")
	must(t, err)
	if !allEnded || record.AgentEndedAt.IsZero() {
		t.Fatal("end is recorded")
	}
	if result, err := registry.Write("s1", "", "a.go", "y\n"); err != nil || !result.Reopened {
		t.Fatalf("its agent may still fix something while it finalises, which reopens it: %+v %v", result, err)
	}
	_, _, err = registry.MarkAgentEnded("s1", "")
	must(t, err)
	_, _, err = registry.RecordCheck("s1", "", store.CheckPassed, "")
	must(t, err)
	if _, err := registry.Write("s1", "", "a.go", "z\n"); err == nil || !strings.Contains(err.Error(), "ready for the user's review") {
		t.Fatalf("edits once it is the user's are refused: %v", err)
	}
	if _, _, err := registry.MarkAgentEnded("nothing", ""); err == nil {
		t.Fatal("ending a session that staged nothing is an error")
	}
}

func TestPendingSessionsSurviveARestart(t *testing.T) {
	registry, root, log := newTestRegistry(t)
	_, err := registry.Write("s1", "", "a.go", "x\n")
	must(t, err)
	_, err = registry.Write("s2", "", "a.go", "y\n")
	must(t, err)
	discarded, err := registry.Discard("s2", "the agent", 20)
	must(t, err)
	if discarded.Status != store.AgentEditDiscarded || discarded.Outcome != "discarded by the agent" {
		t.Fatalf("discarded = %+v", discarded)
	}

	restarted := NewRegistry(root, log)
	must(t, restarted.Load())
	pending := restarted.Pending()
	if len(pending) != 1 || pending[0].Session != "s1" || pending[0].Files[0].Content != "x\n" {
		t.Fatalf("pending after restart = %+v", pending)
	}
}

func TestFinishedSessionsAreTrimmedOldestFirst(t *testing.T) {
	registry, _, log := newTestRegistry(t)
	for _, id := range []string{"s1", "s2", "s3"} {
		_, err := registry.Write(id, "", "a.go", id+"\n")
		must(t, err)
	}
	_, err := registry.Write("open", "", "a.go", "still staging\n")
	must(t, err)
	for _, id := range []string{"s1", "s2", "s3"} {
		_, err := registry.Discard(id, "the user", 2)
		must(t, err)
	}
	finished, _ := log.LoadFinishedAgentEdits()
	if len(finished) != 2 || finished[0].Session != "s3" || finished[1].Session != "s2" {
		t.Fatalf("keep 2 keeps the newest, newest first: %+v", finished)
	}
	pending, _ := log.LoadPendingAgentEdits()
	if len(pending) != 1 || pending[0].Session != "open" {
		t.Fatal("trimming never touches a pending session")
	}
}

func TestLineChanges(t *testing.T) {
	for _, test := range []struct {
		previous, next                  string
		lineFrom, lineTo, added, remove int
	}{
		{"a\nb\nc\n", "a\nb\nc\n", 0, 0, 0, 0},
		{"a\nb\nc\n", "a\nB\nc\n", 2, 2, 1, 1},
		{"a\nb\nc\n", "a\nc\n", 2, 2, 0, 1},
		{"a\nb\n", "a\n", 1, 1, 0, 1},
		{"", "a\nb\n", 1, 2, 2, 0},
	} {
		lineFrom, lineTo, added, removed := LineChanges(test.previous, test.next)
		if lineFrom != test.lineFrom || lineTo != test.lineTo || added != test.added || removed != test.remove {
			t.Fatalf("LineChanges(%q, %q) = %d %d +%d -%d", test.previous, test.next, lineFrom, lineTo, added, removed)
		}
	}
}

func TestConcurrentEditsAndReads(t *testing.T) {
	registry, _, log := newTestRegistry(t)
	done := make(chan struct{})
	for writer := range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for index := range 20 {
				rel := "f" + string(rune('a'+writer)) + ".go"
				if _, err := registry.Write("s1", "", rel, strings.Repeat("x\n", index+1)); err != nil {
					t.Error(err)
					return
				}
				_ = registry.Pending()
			}
		}()
	}
	for range 8 {
		<-done
	}
	record, _ := registry.Snapshot("s1")
	if len(record.Files) != 8 {
		t.Fatalf("8 files staged, got %d", len(record.Files))
	}
	saved, _ := log.LoadPendingAgentEdits()
	if len(saved) != 1 || len(saved[0].Files) != 8 {
		t.Fatal("the saved record is the last one published")
	}
	for _, file := range record.Files {
		if len(file.Edits) != 20 || file.Content != strings.Repeat("x\n", 20) {
			t.Fatalf("%s: %d edits, content %q", file.Rel, len(file.Edits), file.Content)
		}
	}
}

func TestAnEditQueuedBehindAFailedFirstEditIsKept(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	// Hold the session's lock as a failing first edit would, then let a
	// good edit queue behind it.
	set, _, err := registry.lockedSetFor("s1")
	must(t, err)
	queued := make(chan error, 1)
	go func() {
		_, err := registry.Write("s1", "", "a.go", "kept\n")
		queued <- err
	}()
	time.Sleep(20 * time.Millisecond) // the write is now waiting on the lock
	registry.dropIfEmpty("s1", set)
	set.saveMu.Unlock()
	must(t, <-queued)
	if content, from, _, _ := registry.Content("s1", "a.go"); from != "s1" || content != "kept\n" {
		t.Fatalf("the queued edit landed on a live session: %q from %q", content, from)
	}
}

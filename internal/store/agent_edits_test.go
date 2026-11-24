package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	nteedb "github.com/nickooan/ntee-db/nteedb-core"
)

func agentEditSession(id string, started time.Time) AgentEditSession {
	return AgentEditSession{
		Session:   id,
		Status:    AgentEditPending,
		StartedAt: started,
		Files: []AgentEditFile{{
			Rel: "a.go", BaseExists: true, BaseContent: "old\n", Content: "new\n",
			Edits: []AgentEdit{{Op: "replace", Old: "old", New: "new", LineFrom: 1, LineTo: 1, Added: 1, Removed: 1, At: started}},
		}},
	}
}

func exerciseAgentEditLog(t *testing.T, log AgentEditLogStore) {
	t.Helper()
	base := time.Unix(1_800_000_000, 0)
	for index, id := range []string{"s1", "s2", "s3", "open"} {
		if err := log.SavePendingAgentEdit(agentEditSession(id, base.Add(time.Duration(index)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	// Saving again replaces the session's record.
	updated := agentEditSession("s1", base)
	updated.Files[0].Content = "newer\n"
	if err := log.SavePendingAgentEdit(updated); err != nil {
		t.Fatal(err)
	}
	pending, err := log.LoadPendingAgentEdits()
	if err != nil || len(pending) != 4 || pending[0].Session != "s1" || pending[0].Files[0].Content != "newer\n" {
		t.Fatalf("pending, oldest first, one record each: %+v %v", pending, err)
	}

	for index, record := range pending[:3] {
		record.Status = AgentEditApplied
		record.EndedAt = base.Add(time.Hour + time.Duration(index)*time.Minute)
		if err := log.FinishAgentEdit(record, 2); err != nil {
			t.Fatal(err)
		}
	}
	finished, _ := log.LoadFinishedAgentEdits()
	if len(finished) != 2 || finished[0].Session != "s3" || finished[1].Session != "s2" {
		t.Fatalf("keep 2: the newest finished, newest first: %+v", finished)
	}
	if finished[0].Files[0].BaseContent != "old\n" || len(finished[0].Files[0].Edits) != 1 {
		t.Fatal("a finished record keeps every file's content and edits")
	}
	pending, _ = log.LoadPendingAgentEdits()
	if len(pending) != 1 || pending[0].Session != "open" {
		t.Fatalf("finishing moves the record; the open session stays pending: %+v", pending)
	}
	if err := log.TrimAgentEdits(1); err != nil {
		t.Fatal(err)
	}
	if finished, _ = log.LoadFinishedAgentEdits(); len(finished) != 1 || finished[0].Session != "s3" {
		t.Fatalf("a lowered keep trims at once: %+v", finished)
	}
}

func TestAgentEditLogWithNteeDB(t *testing.T) {
	exerciseAgentEditLog(t, &AgentEditLog{dir: t.TempDir()})
}

func TestMemoryAgentEditLog(t *testing.T) {
	exerciseAgentEditLog(t, NewMemoryAgentEditLog())
}

func TestAgentEditAgentsRoundTripAndOlderRecordsGetOne(t *testing.T) {
	log := &AgentEditLog{dir: t.TempDir()}
	started := time.Unix(1_800_000_000, 0)
	older := agentEditSession("older", started) // saved the way records were before agents
	older.AgentEndedAt = started.Add(time.Minute)
	shared := agentEditSession("shared", started.Add(time.Hour))
	shared.Agents = []AgentEditParticipant{{Agent: "agent-a", JoinedAt: started, EndedAt: started.Add(time.Minute)}, {Agent: "agent-b", JoinedAt: started, Stopped: false}}
	shared.Files[0].Edits[0].Agent = "agent-b"
	for _, record := range []AgentEditSession{older, shared} {
		if err := log.SavePendingAgentEdit(record); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := log.LoadPendingAgentEdits()
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %+v %v", pending, err)
	}
	if agents := pending[0].Agents; len(agents) != 1 || agents[0].Agent != "" || !agents[0].EndedAt.Equal(older.AgentEndedAt) {
		t.Fatalf("an older record loads with one unnamed agent: %+v", agents)
	}
	if agents := pending[1].Agents; len(agents) != 2 || agents[1].Agent != "agent-b" || !agents[1].Editing() || pending[1].Files[0].Edits[0].Agent != "agent-b" {
		t.Fatalf("agents and each edit's agent round-trip: %+v", pending[1])
	}
	if editing := pending[1].EditingAgents(); len(editing) != 1 || editing[0] != "agent-b" {
		t.Fatalf("editing = %v", editing)
	}
}

// bulkySession is a pending session whose one file holds size bytes.
func bulkySession(id string, started time.Time, size int) AgentEditSession {
	session := agentEditSession(id, started)
	session.Files[0].Content = strings.Repeat("x", size)
	return session
}

func logSizes(t *testing.T, log *AgentEditLog) (main, blobs int64) {
	t.Helper()
	if err := log.withDB(func(db *nteedb.DB) error {
		stats := db.Stats()
		main, blobs = stats.MainBytes, stats.BlobBytes
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return main, blobs
}

func lowerTidyFloor(t *testing.T, floor int64) {
	t.Helper()
	previous := tidyMinBytes
	tidyMinBytes = floor
	t.Cleanup(func() { tidyMinBytes = previous })
}

func TestFinishingCompactsALogThatIsMostlyDead(t *testing.T) {
	lowerTidyFloor(t, 64<<10)
	log := &AgentEditLog{dir: t.TempDir()}
	started := time.Unix(1_800_000_000, 0)
	// Twenty rewrites of one pending session, the way staged edits save it.
	for edit := range 20 {
		session := bulkySession("s1", started, 8<<10)
		session.Files[0].Content += strings.Repeat("y", edit)
		if err := log.SavePendingAgentEdit(session); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := logSizes(t, log)
	finished := bulkySession("s1", started, 8<<10)
	finished.Status, finished.EndedAt = AgentEditApplied, started.Add(time.Minute)
	if err := log.FinishAgentEdit(finished, 20); err != nil {
		t.Fatal(err)
	}
	after, _ := logSizes(t, log)
	if after*4 > before {
		t.Fatalf("the finish compacted the log: %d → %d bytes", before, after)
	}
	if sessions, err := log.LoadFinishedAgentEdits(); err != nil || len(sessions) != 1 || len(sessions[0].Files[0].Content) != 8<<10 {
		t.Fatalf("the live record survives: %v %d", err, len(sessions))
	}
}

func TestTidyLeavesASmallOrHealthyLogAlone(t *testing.T) {
	log := &AgentEditLog{dir: t.TempDir()}
	started := time.Unix(1_800_000_000, 0)
	for edit := range 5 {
		if err := log.SavePendingAgentEdit(bulkySession("s1", started, 1<<10+edit)); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := log.Tidy(); err != nil || result.Compacted || result.Relieved || result.After != result.Before {
		t.Fatalf("under the floor nothing is rewritten: %+v %v", result, err)
	}
	lowerTidyFloor(t, 1)
	healthy := &AgentEditLog{dir: t.TempDir()}
	for index := range 5 {
		if err := healthy.SavePendingAgentEdit(bulkySession("s"+string(rune('a'+index)), started.Add(time.Duration(index)*time.Second), 1<<10)); err != nil {
			t.Fatal(err)
		}
	}
	if result, err := healthy.Tidy(); err != nil || result.Compacted || result.Relieved {
		t.Fatalf("an all-live log is left alone: %+v %v", result, err)
	}
}

func TestTidyRelievesOrphanedBlobs(t *testing.T) {
	lowerTidyFloor(t, 64<<10)
	log := &AgentEditLog{dir: t.TempDir()}
	started := time.Unix(1_800_000_000, 0)
	// Records past ntee-db's 64 KiB blob threshold land in blobs.dat, which
	// only a relieve reclaims.
	for edit := range 6 {
		session := bulkySession("big", started, 80<<10)
		session.Files[0].Content += strings.Repeat("y", edit)
		if err := log.SavePendingAgentEdit(session); err != nil {
			t.Fatal(err)
		}
	}
	_, blobsBefore := logSizes(t, log)
	result, err := log.Tidy()
	if err != nil || !result.Relieved || result.After >= result.Before {
		t.Fatalf("orphaned blobs are relieved: %+v %v", result, err)
	}
	if _, blobsAfter := logSizes(t, log); blobsAfter*3 > blobsBefore {
		t.Fatalf("blobs.dat shrinks: %d → %d", blobsBefore, blobsAfter)
	}
	if sessions, err := log.LoadPendingAgentEdits(); err != nil || len(sessions) != 1 || len(sessions[0].Files[0].Content) != 80<<10+5 {
		t.Fatalf("the live record survives: %v %d", err, len(sessions))
	}
}

func TestLoweringTheKeepLimitTidiesToo(t *testing.T) {
	lowerTidyFloor(t, 32<<10)
	log := &AgentEditLog{dir: t.TempDir()}
	started := time.Unix(1_800_000_000, 0)
	for index := range 10 {
		session := bulkySession("s"+string(rune('a'+index)), started.Add(time.Duration(index)*time.Minute), 8<<10)
		session.Status, session.EndedAt = AgentEditApplied, started.Add(time.Hour+time.Duration(index)*time.Minute)
		if err := log.FinishAgentEdit(session, 20); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := logSizes(t, log)
	if err := log.TrimAgentEdits(1); err != nil {
		t.Fatal(err)
	}
	after, _ := logSizes(t, log)
	if after*3 > before {
		t.Fatalf("trimming to 1 compacts the log: %d → %d", before, after)
	}
	if sessions, _ := log.LoadFinishedAgentEdits(); len(sessions) != 1 {
		t.Fatalf("one finished session is kept: %d", len(sessions))
	}
}

func TestTheMemoryLogHasNothingToTidy(t *testing.T) {
	if result, err := NewMemoryAgentEditLog().Tidy(); err != nil || result != (TidyResult{}) {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestAPendingSessionSavesOnlyTheFileThatChanged(t *testing.T) {
	log := &AgentEditLog{dir: t.TempDir()}
	session := agentEditSession("S", time.Unix(1_800_000_000, 0))
	session.Files = append(session.Files, AgentEditFile{Rel: "b.go", Content: strings.Repeat("b", 100_000), Edits: []AgentEdit{}})
	must(t, log.SavePendingAgentEditFiles(session, []string{session.Files[0].Rel}))
	_, first := logSizes(t, log)
	mainBefore, _ := logSizes(t, log)

	session.Files[0].Content = "changed"
	must(t, log.SavePendingAgentEditFiles(session, []string{session.Files[0].Rel}))
	mainAfter, blobsAfter := logSizes(t, log)
	if grown := (mainAfter - mainBefore) + (blobsAfter - first); grown > 20_000 {
		t.Fatalf("an edit to one file does not rewrite the other's 100 kB: grew %d bytes", grown)
	}
	session.AgentEndedAt = session.StartedAt.Add(time.Minute)
	must(t, log.SavePendingAgentEditFiles(session, nil)) // the session's own fields only

	pending, err := log.LoadPendingAgentEdits()
	must(t, err)
	if len(pending) != 1 || len(pending[0].Files) != 2 || pending[0].Files[0].Content != "changed" ||
		len(pending[0].Files[1].Content) != 100_000 || !pending[0].AgentEndedAt.Equal(session.AgentEndedAt) {
		t.Fatalf("the files load back in order, with the session's fields: %+v", pending)
	}

	session.Status, session.EndedAt = AgentEditApplied, session.StartedAt.Add(time.Hour)
	must(t, log.FinishAgentEdit(session, 10))
	must(t, log.withDB(func(db *nteedb.DB) error {
		left, err := db.PrefixScan(agentEditPendingFilePrefix)
		if len(left) != 0 {
			t.Fatalf("finishing drops the files kept apart: %v", left)
		}
		return err
	}))
}

func TestALegacyInlineRecordIsSavedWholeOnItsNextSave(t *testing.T) {
	dir := t.TempDir()
	session := agentEditSession("old", time.Unix(1_800_000_000, 0))
	legacy, _ := json.Marshal(session) // files inline, no fileRels
	writer := &AgentEditLog{dir: dir}
	must(t, writer.withDB(func(db *nteedb.DB) error { return db.Put(agentEditPendingKey(session), legacy) }))

	restarted := &AgentEditLog{dir: dir}
	pending, err := restarted.LoadPendingAgentEdits()
	must(t, err)
	if len(pending) != 1 || pending[0].Files[0].Content != session.Files[0].Content {
		t.Fatalf("a legacy record loads its inline files: %+v", pending)
	}
	pending[0].AgentEndedAt = session.StartedAt.Add(time.Minute)
	must(t, restarted.SavePendingAgentEditFiles(pending[0], nil))
	again, err := (&AgentEditLog{dir: dir}).LoadPendingAgentEdits()
	must(t, err)
	if len(again) != 1 || len(again[0].Files) != 1 || again[0].Files[0].Content != session.Files[0].Content {
		t.Fatalf("a header-only save of a legacy record still writes its files: %+v %v", again, err)
	}
}

func TestFinishedSessionsListAsSummariesAndLoadWholeForATrace(t *testing.T) {
	dir := t.TempDir()
	log := &AgentEditLog{dir: dir}
	started := time.Unix(1_800_000_000, 0)
	legacy := agentEditSession("legacy", started)
	legacy.Status, legacy.EndedAt = AgentEditApplied, started.Add(time.Minute)
	legacyData, _ := json.Marshal(legacy) // finished before summaries were kept
	must(t, log.withDB(func(db *nteedb.DB) error { return db.Put(agentEditDoneKey(legacy), legacyData) }))
	fresh := agentEditSession("fresh", started.Add(time.Hour))
	fresh.Status, fresh.EndedAt = AgentEditDiscarded, started.Add(2*time.Hour)
	must(t, log.FinishAgentEdit(fresh, 10))

	summaries, err := log.LoadFinishedAgentEditSummaries()
	must(t, err)
	if len(summaries) != 2 || summaries[0].Session != "fresh" || summaries[1].Session != "legacy" {
		t.Fatalf("newest first: %+v", summaries)
	}
	for _, summary := range summaries {
		if file := summary.Files[0]; file.Rel != "a.go" || file.Content != "" || file.BaseContent != "" || len(file.Edits) != 0 {
			t.Fatalf("a summary keeps paths and results, not contents: %+v", file)
		}
	}
	must(t, log.withDB(func(db *nteedb.DB) error {
		_, ok, err := db.Get(agentEditSummaryKey(agentEditDoneKey(legacy)))
		if !ok {
			t.Fatal("the legacy session got its summary on the first list")
		}
		return err
	}))

	whole, found, err := log.LoadFinishedAgentEdit("legacy")
	must(t, err)
	if !found || whole.Files[0].Content != "new\n" || len(whole.Files[0].Edits) != 1 {
		t.Fatalf("a trace loads the whole record: %+v %v", whole, found)
	}
	if _, found, _ := log.LoadFinishedAgentEdit("gacy"); found {
		t.Fatal("only the exact session id matches")
	}

	must(t, log.TrimAgentEdits(1))
	if summaries, _ := log.LoadFinishedAgentEditSummaries(); len(summaries) != 1 || summaries[0].Session != "fresh" {
		t.Fatalf("trimming drops the summary with its record: %+v", summaries)
	}
	must(t, log.withDB(func(db *nteedb.DB) error {
		left, err := db.PrefixScan(agentEditSummaryPrefix)
		if len(left) != 1 {
			t.Fatalf("one summary left: %v", left)
		}
		return err
	}))
}

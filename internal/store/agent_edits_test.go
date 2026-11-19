package store

import (
	"testing"
	"time"
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

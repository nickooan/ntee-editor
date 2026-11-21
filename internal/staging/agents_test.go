package staging

import (
	"strings"
	"testing"
	"time"

	"github.com/nickooan/ntee-editor/internal/store"
)

func TestAgentsShareASessionAndAllMustEnd(t *testing.T) {
	registry, _, log := newTestRegistry(t)
	first, err := registry.Replace("S", "agent-a", "a.go", Replacement{Old: "one", New: "ONE"})
	must(t, err)
	if first.Joined || len(first.OtherAgents) != 0 {
		t.Fatalf("the first agent starts the session: %+v", first)
	}
	joined, err := registry.Replace("S", "agent-b", "a.go", Replacement{Old: "three", New: "THREE"})
	must(t, err)
	if !joined.Joined || strings.Join(joined.OtherAgents, ",") != "agent-a" {
		t.Fatalf("the second agent joins: %+v", joined)
	}
	if content := stagedContent(t, registry, "S", "a.go"); content != "ONE\ntwo\nTHREE\ntwo\n" {
		t.Fatalf("both edits land on the session's copy: %q", content)
	}
	record, _ := registry.Snapshot("S")
	if len(record.Agents) != 2 || record.Files[0].Edits[0].Agent != "agent-a" || record.Files[0].Edits[1].Agent != "agent-b" {
		t.Fatalf("each edit records its agent: %+v", record)
	}

	if _, _, err := registry.MarkAgentEnded("S", "agent-c"); err == nil || !strings.Contains(err.Error(), "agent agent-c has no edits in edit session S") {
		t.Fatalf("an agent with no edits cannot end: %v", err)
	}
	record, allEnded, err := registry.MarkAgentEnded("S", "agent-a")
	must(t, err)
	if allEnded || !record.AgentEndedAt.IsZero() || strings.Join(record.EditingAgents(), ",") != "agent-b" {
		t.Fatalf("one end of two is partial: %v %+v", allEnded, record.Agents)
	}
	if _, err := registry.Write("S", "agent-a", "c.go", "c\n"); err != nil {
		t.Fatalf("an ended agent may take its part up again while the session is open: %v", err)
	}
	if record, _ := registry.Snapshot("S"); len(record.EditingAgents()) != 2 {
		t.Fatalf("and is editing again: %+v", record.Agents)
	}
	_, _, err = registry.MarkAgentEnded("S", "agent-a")
	must(t, err)
	record, allEnded, err = registry.MarkAgentEnded("S", "agent-b")
	must(t, err)
	if !allEnded || record.AgentEndedAt.IsZero() {
		t.Fatalf("the last end readies the session: %+v", record)
	}
	if pending, _ := log.LoadPendingAgentEdits(); len(pending) != 1 || len(pending[0].Agents) != 2 || pending[0].Agents[1].EndedAt.IsZero() {
		t.Fatalf("the agents are saved: %+v", pending)
	}
	if record.Phase() != store.PhaseFinalising {
		t.Fatalf("every agent ended: the session finalises (%s)", record.Phase())
	}
	if _, err := registry.Write("S", "agent-c", "a.go", "late\n"); err == nil || !strings.Contains(err.Error(), "edit session S is finalising (its agents are checking it) — it is no longer open to join") {
		t.Fatalf("a newcomer is turned away while it finalises: %v", err)
	}
}

func TestAgentsCheckAFinalisingSessionBeforeTheUserIsAsked(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	_, err := registry.Write("S", "agent-a", "a.go", "a\n")
	must(t, err)
	_, err = registry.Write("S", "agent-b", "b.go", "b\n")
	must(t, err)
	if _, _, err := registry.RecordCheck("S", "agent-a", store.CheckPassed, ""); err == nil || !strings.Contains(err.Error(), "still being edited") {
		t.Fatalf("no check while agents edit: %v", err)
	}
	_, _, err = registry.MarkAgentEnded("S", "agent-a")
	must(t, err)
	_, _, err = registry.MarkAgentEnded("S", "agent-b")
	must(t, err)

	record, allChecked, err := registry.RecordCheck("S", "agent-a", store.CheckPassed, "go test ./... ok")
	must(t, err)
	if allChecked || record.Phase() != store.PhaseFinalising || strings.Join(record.CheckingAgents(), ",") != "agent-b" {
		t.Fatalf("one check of two: %v %+v", allChecked, record.Agents)
	}
	if _, _, err := registry.RecordCheck("S", "agent-c", store.CheckPassed, ""); err == nil || !strings.Contains(err.Error(), "not in edit session S") {
		t.Fatalf("only the session's agents check it: %v", err)
	}

	// agent-b's check fails and it fixes the code: the session reopens and
	// every check is due again.
	fix, err := registry.Write("S", "agent-b", "b.go", "fixed\n")
	must(t, err)
	record, _ = registry.Snapshot("S")
	if !fix.Reopened || record.Phase() != store.PhaseEditing || record.Agents[0].Check != "" || record.Agents[0].Editing() {
		t.Fatalf("the fix reopens the session, clears the checks, and leaves agent-a ended: %+v", record.Agents)
	}
	_, _, err = registry.MarkAgentEnded("S", "agent-b")
	must(t, err)
	_, _, err = registry.RecordCheck("S", "agent-a", store.CheckPassed, "")
	must(t, err)
	record, allChecked, err = registry.RecordCheck("S", "agent-b", store.CheckFailed, "npm test: 1 failing")
	must(t, err)
	if !allChecked || record.Phase() != store.PhaseReady || record.ReadyAt.IsZero() || record.Agents[1].CheckNote != "npm test: 1 failing" {
		t.Fatalf("the last check readies it for the user: %+v", record)
	}
	if _, err := registry.Write("S", "agent-a", "a.go", "late\n"); err == nil || !strings.Contains(err.Error(), "ready for the user's review") {
		t.Fatalf("no edits once it is the user's: %v", err)
	}
}

func TestForceEndStopsTheAgentsStillEditing(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	_, err := registry.Write("S", "agent-a", "a.go", "a\n")
	must(t, err)
	_, err = registry.Write("S", "agent-b", "b.go", "b\n")
	must(t, err)
	_, _, err = registry.MarkAgentEnded("S", "agent-a")
	must(t, err)
	record, waitedFor, err := registry.ForceEnd("S")
	must(t, err)
	if strings.Join(waitedFor, ",") != "agent-a,agent-b" || record.Phase() != store.PhaseReady || !record.Agents[1].Stopped || record.Agents[0].Stopped || record.Agents[0].Check != store.CheckSkipped {
		t.Fatalf("the agent still editing is stopped, the pending check skipped: %v %+v", waitedFor, record.Agents)
	}
	if _, err := registry.Write("S", "agent-b", "b.go", "more\n"); err == nil || !strings.Contains(err.Error(), "the user stopped waiting for agents in edit session S") {
		t.Fatalf("a stopped agent's next edit is refused: %v", err)
	}
	if _, allEnded, err := registry.MarkAgentEnded("S", "agent-b"); err != nil || !allEnded {
		t.Fatalf("its late end is harmless: %v %v", allEnded, err)
	}
}

func TestARecordFromBeforeAgentsLoadsWithOneUnnamedAgent(t *testing.T) {
	started, ended := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)
	legacy := store.AgentEditSession{Session: "old", StartedAt: started, AgentEndedAt: ended, Files: []store.AgentEditFile{{Rel: "a.go"}}}.WithAgents()
	if len(legacy.Agents) != 1 || legacy.Agents[0].Agent != "" || legacy.Agents[0].JoinedAt != started || legacy.Agents[0].EndedAt != ended {
		t.Fatalf("legacy agents = %+v", legacy.Agents)
	}
	open := store.AgentEditSession{Session: "open", StartedAt: started, Files: []store.AgentEditFile{{Rel: "a.go"}}}.WithAgents()
	if strings.Join(open.EditingAgents(), ",") != "" || len(open.EditingAgents()) != 1 {
		t.Fatalf("a legacy session not ended yet has its unnamed agent editing: %+v", open.Agents)
	}
}

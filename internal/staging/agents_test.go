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
	if _, err := registry.Write("S", "agent-b", "a.go", "late\n"); err == nil || !strings.Contains(err.Error(), "already got end from every agent") {
		t.Fatalf("edits after the last end are refused: %v", err)
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
	record, stopped, err := registry.ForceEnd("S")
	must(t, err)
	if strings.Join(stopped, ",") != "agent-b" || record.AgentEndedAt.IsZero() || !record.Agents[1].Stopped || record.Agents[0].Stopped {
		t.Fatalf("only the agent still editing is stopped: %v %+v", stopped, record.Agents)
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

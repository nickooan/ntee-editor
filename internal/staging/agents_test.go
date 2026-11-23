package staging

import (
	"encoding/json"
	"os"
	"slices"
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

// readySession stages a.go and b.go (or the given files) by two agents and
// walks the session through end and check to ready.
func readySession(t *testing.T, registry *Registry, files ...string) {
	t.Helper()
	files = append(files, "a.go", "b.go")
	_, err := registry.Write("S", "agent-a", files[0], "a\n")
	must(t, err)
	_, err = registry.Write("S", "agent-b", files[1], "b\n")
	must(t, err)
	for _, agent := range []string{"agent-a", "agent-b"} {
		_, _, err = registry.MarkAgentEnded("S", agent)
		must(t, err)
	}
	for _, agent := range []string{"agent-a", "agent-b"} {
		_, _, err = registry.RecordCheck("S", agent, store.CheckPassed, "ok")
		must(t, err)
	}
}

func TestRequestChangesReopensAReadySessionForItsAgents(t *testing.T) {
	registry, _, log := newTestRegistry(t)
	if _, _, err := registry.RequestChanges("S"); err == nil || !strings.Contains(err.Error(), "no edit session S") {
		t.Fatalf("an unknown session is refused: %v", err)
	}
	_, err := registry.Write("S", "agent-a", "a.go", "a\n")
	must(t, err)
	if _, _, err := registry.RequestChanges("S"); err == nil || !strings.Contains(err.Error(), "edit session S is still editing") {
		t.Fatalf("a session still being edited is refused: %v", err)
	}
	_, _, err = registry.MarkAgentEnded("S", "agent-a")
	must(t, err)
	if _, _, err := registry.RequestChanges("S"); err == nil || !strings.Contains(err.Error(), "still finalising") {
		t.Fatalf("a finalising session is refused: %v", err)
	}
	_, err = registry.Write("S", "agent-b", "b.go", "b\n")
	if err == nil {
		t.Fatal("a newcomer cannot join a finalising session")
	}
	registry, _, log = newTestRegistry(t)
	readySession(t, registry)

	record, reopened, err := registry.RequestChanges("S")
	must(t, err)
	if strings.Join(reopened, ",") != "agent-a,agent-b" || record.Phase() != store.PhaseEditing || !record.ReadyAt.IsZero() || len(record.EditingAgents()) != 2 {
		t.Fatalf("every agent is editing again: %v %+v", reopened, record)
	}
	if record.Agents[0].Check != "" || record.Agents[1].CheckNote != "" {
		t.Fatalf("the checks are cleared: %+v", record.Agents)
	}
	if request, open := record.OpenChangeRequest(); !open || request.At.IsZero() || len(record.ChangeRequests) != 1 {
		t.Fatalf("the request is logged and open: %+v", record.ChangeRequests)
	}
	if pending, _ := log.LoadPendingAgentEdits(); len(pending) != 1 || len(pending[0].ChangeRequests) != 1 {
		t.Fatalf("the request is saved: %+v", pending)
	}

	if _, err := registry.Replace("S", "agent-b", "b.go", Replacement{Old: "b", New: "B"}); err != nil {
		t.Fatalf("an agent's follow-up edit goes into the same session: %v", err)
	}
	record, _ = registry.Snapshot("S")
	if request, open := record.OpenChangeRequest(); open || request.AnsweredBy != "agent-b" || request.AnsweredAt.IsZero() {
		t.Fatalf("the edit answers the request: %+v", record.ChangeRequests)
	}
	if stagedContent(t, registry, "S", "b.go") != "B\n" {
		t.Fatalf("the follow-up stacks on the staged copy")
	}
}

func TestAnEndAnswersAChangeRequest(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	readySession(t, registry)
	_, _, err := registry.RequestChanges("S")
	must(t, err)
	record, allEnded, err := registry.MarkAgentEnded("S", "agent-a")
	must(t, err)
	if allEnded {
		t.Fatal("agent-b is still editing after the request")
	}
	if request, open := record.OpenChangeRequest(); open || request.AnsweredBy != "agent-a" {
		t.Fatalf("an end answers the request too: %+v", record.ChangeRequests)
	}
}

func TestRequestChangesNeedsAnAgentTheUserDidNotStop(t *testing.T) {
	registry, _, _ := newTestRegistry(t)
	_, err := registry.Write("S", "agent-a", "a.go", "a\n")
	must(t, err)
	_, _, err = registry.ForceEnd("S")
	must(t, err)
	if _, _, err := registry.RequestChanges("S"); err == nil || !strings.Contains(err.Error(), "no agent left in edit session S to ask") {
		t.Fatalf("with every agent stopped there is no one to ask: %v", err)
	}
	record, _ := registry.Snapshot("S")
	if record.Phase() != store.PhaseReady || len(record.ChangeRequests) != 0 {
		t.Fatalf("the refused request changes nothing: %+v", record)
	}
}

func TestRequestChangesDropsTheSandbox(t *testing.T) {
	registry, _, boxes, _ := sandboxWorkspace(t)
	readySession(t, registry, "repoA/a.go", "repoB/b.go")
	if _, err := registry.Sandbox("S", false, nil); err != nil {
		t.Fatal(err)
	}
	_, _, err := registry.RequestChanges("S")
	must(t, err)
	if entries, _ := os.ReadDir(boxes); slices.ContainsFunc(entries, func(entry os.DirEntry) bool { return entry.Name() == "S" }) {
		t.Fatalf("the sandbox is dropped: %v", entries)
	}
}

func TestChangeRequestsAreStoredExplicitly(t *testing.T) {
	registry, _, log := newTestRegistry(t)
	_, err := registry.Write("S", "agent-a", "a.go", "a\n")
	must(t, err)
	pending, _ := log.LoadPendingAgentEdits()
	data, err := json.Marshal(pending[0])
	must(t, err)
	if !strings.Contains(string(data), `"changeRequests":[]`) {
		t.Fatalf("a new session stores an empty log, not null: %s", data)
	}
	var legacy store.AgentEditSession
	must(t, json.Unmarshal([]byte(`{"session":"old","status":"pending","files":[]}`), &legacy))
	if legacy = legacy.WithStoredDefaults(); legacy.ChangeRequests == nil {
		t.Fatal("a legacy record loads with an empty log")
	}
	if _, open := legacy.OpenChangeRequest(); open {
		t.Fatal("and nothing open")
	}
}

func TestFinishKeepsTheGitFollowUpExplicitly(t *testing.T) {
	registry, _, log := newTestRegistry(t)
	_, err := registry.Write("S", "agent-a", "a.go", "a\n")
	must(t, err)
	if pending, _ := log.LoadPendingAgentEdits(); pending[0].GitFollowUp != store.GitFollowUpNone {
		t.Fatalf("a new session stores none: %q", pending[0].GitFollowUp)
	}
	_, _, err = registry.MarkAgentEnded("S", "agent-a")
	must(t, err)
	record, err := registry.Finish("S", store.AgentEditPolicyReview, store.GitFollowUpPullRequest, registry.ApplyFiles("S", mustSnapshot(t, registry, "S").Files, nil), 20)
	must(t, err)
	if record.GitFollowUp != store.GitFollowUpPullRequest {
		t.Fatalf("finish records the follow-up: %q", record.GitFollowUp)
	}

	_, err = registry.Write("D", "agent-a", "a.go", "d\n")
	must(t, err)
	discarded, err := registry.Discard("D", "the user", 20)
	must(t, err)
	data, err := json.Marshal(discarded)
	must(t, err)
	if !strings.Contains(string(data), `"gitFollowUp":"none"`) {
		t.Fatalf("a discard stores none explicitly: %s", data)
	}

	_, err = registry.Write("E", "agent-a", "a.go", "e\n")
	must(t, err)
	if empty, err := registry.Finish("E", store.AgentEditPolicyApply, "", nil, 20); err != nil || empty.GitFollowUp != store.GitFollowUpNone {
		t.Fatalf("no follow-up given is none: %q %v", empty.GitFollowUp, err)
	}

	var legacy store.AgentEditSession
	must(t, json.Unmarshal([]byte(`{"session":"old","status":"applied","files":[]}`), &legacy))
	if legacy.WithStoredDefaults().GitFollowUp != store.GitFollowUpNone {
		t.Fatal("a legacy record loads as none")
	}
}

func mustSnapshot(t *testing.T, registry *Registry, session string) store.AgentEditSession {
	t.Helper()
	record, ok := registry.Snapshot(session)
	if !ok {
		t.Fatalf("no pending session %s", session)
	}
	return record
}

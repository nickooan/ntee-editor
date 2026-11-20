package app

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

func agentReplace(session, agent, path string, payload extmsg.ReplacePayload) string {
	return "replace --edit-session=" + session + " --agent=" + agent + "\n" + path + "\n---\n" + extmsg.EncodeReplace(payload)
}

// sharedSession has agents a and b editing session S: a changes main.go,
// b joins and adds lib/util.ts.
func sharedSession(t *testing.T) (Model, string, *ExternalReceiver) {
	t.Helper()
	m, root, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle(agentReplace("S", "agent-a", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	reply := receiver.Handle(agentReplace("S", "agent-b", "lib/util.ts", extmsg.ReplacePayload{Old: "= 1", New: "= 2"}))
	if !strings.Contains(reply, "joined edit session S (still editing: agent-a) — the user reviews it once every agent in it sends end") {
		t.Fatalf("b hears it joined: %q", reply)
	}
	next, _ := m.Update(agentEditsChangedMsg{session: "S"})
	return next.(Model), root, receiver
}

// endAs sends an agent's end the way the socket does: a partial end is
// answered by the receiver; the last goes on to the UI.
func endAs(t *testing.T, m Model, receiver *ExternalReceiver, agent string) (Model, string, chan string) {
	t.Helper()
	record, allEnded, err := receiver.agentEdits.MarkAgentEnded("S", agent)
	must(t, err)
	if !allEnded {
		next, _ := m.Update(agentEditsChangedMsg{session: "S"})
		return next.(Model), extmsg.OkReply(agentEditPartEndedText(record)), nil
	}
	reply := make(chan string, 1)
	m, cmd := deliver(m, externalMessageMsg{
		record: sessionRecord(extmsg.CommandEnd, "S", "", ""), sessionKind: extmsg.SessionEdit, reply: reply,
		agentEdits: agentEditSettings{policy: store.AgentEditPolicyReview, keep: 20},
	})
	return settleAgentEdits(m, cmd), "", reply
}

func TestASharedSessionWaitsForEveryAgent(t *testing.T) {
	m, root, receiver := sharedSession(t)
	if header := headerLine(m); !strings.Contains(header, "✎ agent editing…") {
		t.Fatalf("the header says agents are editing: %q", header)
	}

	reply := receiver.Handle("end --edit-session=S --agent=agent-a\n")
	if !strings.HasPrefix(reply, "ok your part of edit session S is done — waiting for 1 other agent (agent-b)") {
		t.Fatalf("a partial end is answered at once: %q", reply)
	}
	next, _ := m.Update(agentEditsChangedMsg{session: "S"})
	m = next.(Model)
	if m.choice.open() {
		t.Fatal("no prompt while an agent still edits")
	}
	if m = m.askApplyAgentEdits("S"); m.choice.open() || !strings.Contains(m.errText, "agents are still editing edit session S (agent-b)") {
		t.Fatalf("applying waits for every agent: choice=%v err=%q", m.choice.open(), m.errText)
	}
	if m, _ = m.startAgentEditApply("S", store.AgentEditPolicyReview, false, 20, nil); diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" {
		t.Fatal("nothing is written")
	}
	if m = m.askDiscardAgentEdits("S"); m.choice.kind != choiceDiscardAgentEdits {
		t.Fatal("discarding is always possible")
	}
	m.choice = choiceOverlay{}

	m, _, endReply := endAs(t, m, receiver, "agent-b")
	if gotReply(t, endReply) == "" || m.choice.kind != choiceAgentEditsReady {
		t.Fatalf("the last end offers the review: %v", m.choice.kind)
	}
	if header := headerLine(m); strings.Contains(header, "agent editing") {
		t.Fatalf("the label goes once every agent ended: %q", header)
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Review edits
	m = settleAgentEdits(next.(Model), cmd)
	file, _ := m.selectedReviewFile()
	if len(file.notes) != 1 || !strings.Contains(file.notes[0].overlay.text, "· agent-") {
		t.Fatalf("a shared session's edit log names the agents: %+v", file.notes)
	}
	m = key(m, typeRune('a'))
	next, cmd = m.Update(keyPress(tea.KeyEnter))
	m = settleAgentEdits(next.(Model), cmd)
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" || diskContent(t, root, "lib/util.ts") != "export const héllo = 2\n" {
		t.Fatal("both agents' edits are written")
	}
}

func TestTheReviewOfASessionStillEditedSaysApplyingWaits(t *testing.T) {
	m, _, _ := sharedSession(t)
	record, _ := m.agentEdits.registry.Snapshot("S")
	m, cmd := m.enterAgentEditReview(record, false)
	m = settleAgentEdits(m, cmd)
	if status := ansi.Strip(m.renderStatusLine()); !strings.Contains(status, "agents still editing · x discard all") || strings.Contains(status, "a apply all") {
		t.Fatalf("the hint waits for the agents: %q", status)
	}
}

func TestTheUserCanStopWaitingForAgents(t *testing.T) {
	m, root, receiver := sharedSession(t)
	next, cmd := m.Update(ctrlKey('t'))
	m = settleAgentEdits(next.(Model), cmd)
	for m.inspectMenu != inspectMenuAgentEdits {
		m = key(m, shiftKey(tea.KeyDown))
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "● 2 of 2 agents editing") {
		t.Fatalf("the panel shows who still edits:\n%s", plain)
	}
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown)) // the session row
	m = key(m, typeRune('f'))
	if m.choice.kind != choiceForceEndAgentEdits || !strings.Contains(m.choice.body[0].text, "still editing: agent-a, agent-b") {
		t.Fatalf("f asks first: %+v", m.choice)
	}
	next, cmd = m.Update(keyPress(tea.KeyEnter)) // Stop waiting
	m = settleAgentEdits(next.(Model), cmd)
	if m.choice.kind != choiceAgentEditsReady || !strings.Contains(m.notice, "stopped waiting for agent-a, agent-b") {
		t.Fatalf("then the review is offered: choice=%v notice=%q", m.choice.kind, m.notice)
	}
	if reply := receiver.Handle(agentReplace("S", "agent-a", "main.go", extmsg.ReplacePayload{Old: "println(1)", New: "println(9)"})); !strings.Contains(reply, "the user stopped waiting for agents in edit session S") {
		t.Fatalf("a stopped agent's edit is refused: %q", reply)
	}
	m.choice = choiceOverlay{}
	m = m.askApplyAgentEdits("S")
	next, cmd = m.Update(keyPress(tea.KeyEnter))
	m = settleAgentEdits(next.(Model), cmd)
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" {
		t.Fatal("what was staged is applied")
	}
}

func TestASharedSessionRowNamesEachAgentsPart(t *testing.T) {
	record := store.AgentEditSession{Session: "S", Files: []store.AgentEditFile{{Rel: "a.go"}}, Agents: []store.AgentEditParticipant{
		{Agent: "agent-a", EndedAt: time.Now()}, {Agent: "agent-b"}, {Agent: "", EndedAt: time.Now(), Stopped: true},
	}}
	row := ansi.Strip(strings.Join(agentEditSessionRow(record, true).pieces, ""))
	if !strings.Contains(row, "● 1 of 3 agents editing  1 file · agent-a done, agent-b editing, unnamed stopped by the user · started") {
		t.Fatalf("row = %q", row)
	}
	if status := agentEditStatusText(record); !strings.Contains(status, "agents: agent-a done · agent-b editing · unnamed stopped by the user") {
		t.Fatalf("status = %q", status)
	}
}

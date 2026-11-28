package app

import (
	"os"
	"path/filepath"
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

// lastCheck records the last agent's check and hands it to the UI the way
// the receiver does once every check is in.
func lastCheck(t *testing.T, m Model, receiver *ExternalReceiver, agent string, result store.AgentEditCheckResult, note string, policy string) (Model, chan string) {
	t.Helper()
	_, allChecked, err := receiver.agentEdits.RecordCheck("S", agent, result, note)
	must(t, err)
	if !allChecked {
		t.Fatal("not the last check")
	}
	reply := make(chan string, 1)
	m, cmd := deliver(m, externalMessageMsg{
		record: sessionRecord(extmsg.CommandCheck, "S", "", ""), sessionKind: extmsg.SessionEdit, reply: reply,
		agentEdits: agentEditSettings{policy: policy, keep: 20},
	})
	return settleAgentEdits(m, cmd), reply
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
	if m, _ = m.startAgentEditApply("S", store.AgentEditPolicyReview, false, store.GitFollowUpNone, 20, nil); diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" {
		t.Fatal("nothing is written")
	}
	if m = m.askDiscardAgentEdits("S"); m.choice.kind != choiceDiscardAgentEdits {
		t.Fatal("discarding is always possible")
	}
	m.choice = choiceOverlay{}

	// The last end finalises it: the agents check it before the user is asked.
	if reply := receiver.Handle("end --edit-session=S --agent=agent-b\n"); !strings.HasPrefix(reply, "ok edit session S is finalising — every agent's edits are in and no one else may join") {
		t.Fatalf("the last end finalises the session: %q", reply)
	}
	waitReply := make(chan string, 1)
	m, _ = deliver(m, externalMessageMsg{record: sessionRecord(extmsg.CommandWait, "S", "", ""), sessionKind: extmsg.SessionEdit, reply: waitReply})
	next, _ = m.Update(agentEditsFinalisingMsg{session: "S"})
	m = next.(Model)
	if outcome := gotReply(t, waitReply); !strings.HasPrefix(outcome, "waiting — edit session S is finalising") {
		t.Fatalf("a waiting agent is asked to check: %q", outcome)
	}
	if header := headerLine(m); !strings.Contains(header, "⚙ agents checking…") || m.choice.open() {
		t.Fatalf("the header says the agents check it, and no prompt yet: %q", header)
	}
	if reply := receiver.Handle(agentReplace("S", "agent-c", "main.go", extmsg.ReplacePayload{Old: "println(1)", New: "println(3)"})); !strings.Contains(reply, "is finalising (its agents are checking it) — it is no longer open to join") {
		t.Fatalf("a newcomer is turned away: %q", reply)
	}
	if reply := receiver.Handle("check --edit-session=S --agent=agent-a pass\n---\ngo test ./... ok\n"); !strings.HasPrefix(reply, "ok check recorded — waiting for 1 other agent's check (agent-b)") {
		t.Fatalf("a check before the last is answered at once: %q", reply)
	}
	m, checkReply := lastCheck(t, m, receiver, "agent-b", store.CheckFailed, "npm test: 1 failing", store.AgentEditPolicyReview)
	if !strings.Contains(gotReply(t, checkReply), "the user is reviewing 2 staged files") || m.mode != modeReview || m.review.agentEdits != "S" {
		t.Fatalf("the last check opens the review: mode=%v %q", m.mode, m.review.agentEdits)
	}
	if !strings.Contains(m.notice, "✓ agent-a passed") || !strings.Contains(m.notice, "✗ agent-b failed") {
		t.Fatalf("the notice sums up each check: %q", m.notice)
	}
	if header := headerLine(m); strings.Contains(header, "agent editing") || strings.Contains(header, "checking") {
		t.Fatalf("the label goes once the session is the user's: %q", header)
	}
	file, _ := m.selectedReviewFile()
	if len(file.notes) != 1 || !strings.Contains(file.notes[0].overlay.text, "· agent-") {
		t.Fatalf("a shared session's edit log names the agents: %+v", file.notes)
	}
	m = key(m, typeRune('a'))
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "✓ agent-a passed — go test ./... ok") || !strings.Contains(plain, "✗ agent-b failed — npm test: 1 failing") {
		t.Fatalf("the apply prompt shows each check:\n%s", plain)
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter))
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
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "editing · agent-a, agent-b") || !strings.Contains(plain, "back to editor") || strings.Contains(plain, "apply all") {
		t.Fatalf("the floating panel says the agents work:\n%s", plain)
	}
	for _, press := range []rune{'a', 'x', 'r'} {
		locked := key(m, typeRune(press))
		if locked.choice.open() || !strings.Contains(locked.errText, "agents are still working on edit session S") {
			t.Fatalf("%q is locked while agents work: choice=%v err=%q", press, locked.choice.open(), locked.errText)
		}
	}
	if left := key(m, keyPress(tea.KeyEscape)); left.mode == modeReview || left.choice.open() {
		t.Fatal("Esc leaves at once while the agents work")
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
	row := ansi.Strip(strings.Join(agentEditSessionRow(record, true, false).pieces, ""))
	if !strings.Contains(row, "● 1 of 3 agents editing  1 file · agent-a done, agent-b editing, unnamed stopped by the user · started") {
		t.Fatalf("row = %q", row)
	}
	if status := agentEditStatusText(record); !strings.Contains(status, "agents: agent-a done · agent-b editing · unnamed stopped by the user") {
		t.Fatalf("status = %q", status)
	}
}

func TestTheReceiverBuildsSandboxesAndNamesSessionsInTheRepo(t *testing.T) {
	_, root, receiver, registry, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	registry.WithSandboxDir(t.TempDir())
	must(t, os.WriteFile(filepath.Join(root, "lib", "other.ts"), []byte("export const other = 1\n"), 0o644))
	receiver.Handle(agentReplace("S", "agent-a", "lib/util.ts", extmsg.ReplacePayload{Old: "= 1", New: "= 2"}))

	// Another agent reading or starting to edit lib hears that S is changing it.
	line, _, _ := extmsg.SplitBodyReply(receiver.Handle("read\nlib/other.ts\n"))
	if detail := extmsg.BodyDetail(line); !strings.HasPrefix(detail, "disk · version ") || !strings.HasSuffix(detail, " · also editing lib: S (agent-a)") {
		t.Fatalf("read names the open session in the repo: %q", line)
	}
	reply := receiver.Handle("write --edit-session=T --agent=agent-b\nlib/new.ts\n---\nexport {}\n")
	if !strings.Contains(reply, "edit session S is also changing lib (still editing: agent-a) — to join it instead, discard this session and edit in session S") {
		t.Fatalf("a new session in the same repo is told about S: %q", reply)
	}

	if reply := receiver.Handle("sandbox --edit-session=S\n"); !strings.Contains(reply, "is still being edited (by agent-a) — its sandbox is built once every agent has sent end") {
		t.Fatalf("no sandbox while an agent edits: %q", reply)
	}
	receiver.Handle("end --edit-session=S --agent=agent-a\n")
	reply = receiver.Handle("sandbox --edit-session=S\n")
	if !strings.HasPrefix(reply, "ok sandbox ") || !strings.Contains(reply, "holds lib (laid out like the project) · 1 staged file in place") || !strings.Contains(reply, "edits made there are not staged") {
		t.Fatalf("sandbox reply = %q", reply)
	}
	box := strings.Fields(strings.TrimPrefix(reply, "ok sandbox "))[0]
	if diskContent(t, box, "lib/util.ts") != "export const héllo = 2\n" || diskContent(t, root, "lib/util.ts") != "export const héllo = 1\n" {
		t.Fatal("the box has the staged content; the project does not")
	}
	if reply := receiver.Handle("sandbox --edit-session=S drop\n"); reply != "ok dropped the sandbox of edit session S" {
		t.Fatalf("drop = %q", reply)
	}
	if reply := receiver.Handle("sandbox --edit-session=nothing\n"); !strings.HasPrefix(reply, "error ") {
		t.Fatalf("no such session: %q", reply)
	}
	if _, sessions := receiver.agentEdits.SessionsInRepo("lib/other.ts"); len(sessions) != 1 || sessions[0].Session != "T" {
		t.Fatalf("a finalising session is no longer offered to join: %+v", sessions)
	}
}

func TestAFixDuringFinalisingReopensTheSession(t *testing.T) {
	m, _, receiver := sharedSession(t)
	receiver.Handle("end --edit-session=S --agent=agent-a\n")
	receiver.Handle("end --edit-session=S --agent=agent-b\n")
	receiver.Handle("check --edit-session=S --agent=agent-a pass\n")
	waitReply := make(chan string, 1)
	m, _ = deliver(m, externalMessageMsg{record: sessionRecord(extmsg.CommandWait, "S", "", ""), sessionKind: extmsg.SessionEdit, reply: waitReply})

	reply := receiver.Handle(agentReplace("S", "agent-b", "lib/util.ts", extmsg.ReplacePayload{Old: "= 2", New: "= 3"}))
	if !strings.Contains(reply, "edit session S is back to editing — its sandbox is gone and every agent checks again once you send end") {
		t.Fatalf("the fixing agent hears it reopened the session: %q", reply)
	}
	next, _ := m.Update(agentEditsReopenedMsg{session: "S", agent: "agent-b"})
	m = next.(Model)
	if outcome := gotReply(t, waitReply); !strings.HasPrefix(outcome, "waiting — edit session S is back to editing (agent agent-b is fixing it)") {
		t.Fatalf("the waiting agent hears it: %q", outcome)
	}
	if header := headerLine(m); !strings.Contains(header, "✎ agent editing…") {
		t.Fatalf("the header is back to editing: %q", header)
	}
	if reply := receiver.Handle("check --edit-session=S --agent=agent-a pass\n"); !strings.Contains(reply, "still being edited (by agent-b)") {
		t.Fatalf("a check of the stale box is refused: %q", reply)
	}
}

func TestAgentAppliesOnlyWhenNoCheckFailed(t *testing.T) {
	for _, test := range []struct {
		result  store.AgentEditCheckResult
		applied bool
	}{{store.CheckPassed, true}, {store.CheckFailed, false}} {
		m, root, receiver := sharedSession(t)
		receiver.Handle("end --edit-session=S --agent=agent-a\n")
		receiver.Handle("end --edit-session=S --agent=agent-b\n")
		receiver.Handle("check --edit-session=S --agent=agent-a pass\n")
		m, reply := lastCheck(t, m, receiver, "agent-b", test.result, "", store.AgentEditPolicyApply)
		written := diskContent(t, root, "lib/util.ts") == "export const héllo = 2\n"
		if written != test.applied {
			t.Fatalf("%s: written=%v", test.result, written)
		}
		outcome := gotReply(t, reply)
		if test.applied && !strings.HasPrefix(outcome, "finished — edit session S applied 2 files") {
			t.Fatalf("all checks passed: applied at once: %q", outcome)
		}
		if !test.applied && (!strings.Contains(outcome, "(a check failed, so the user decides)") || m.mode != modeReview || !strings.Contains(m.notice, "agent-b's check failed — review it")) {
			t.Fatalf("a failed check leaves it to the user: %q mode=%v notice=%q", outcome, m.mode, m.notice)
		}
	}
}

func TestTheLastCheckWithANoteGoesToTheUI(t *testing.T) {
	_, _, receiver := sharedSession(t)
	receiver.Handle("end --edit-session=S --agent=agent-a\n")
	receiver.Handle("end --edit-session=S --agent=agent-b\n")
	receiver.Handle("check --edit-session=S --agent=agent-a pass\n---\nok\n")
	replies := make(chan string, 1)
	go func() {
		replies <- receiver.Handle("check --edit-session=S --agent=agent-b fail\n---\nnpm test: 1 failing\n")
	}()
	select {
	case queued := <-receiver.queue:
		if queued.record.Command != extmsg.CommandCheck || queued.record.Session != "S" {
			t.Fatalf("queued = %+v", queued.record)
		}
		queued.reply <- "edit session S ended — the user is reviewing 2 staged files"
	case <-time.After(2 * time.Second):
		t.Fatal("the last check, with its note, reaches the UI")
	}
	if reply := <-replies; reply != "ok edit session S ended — the user is reviewing 2 staged files" {
		t.Fatalf("the checker hears the UI's answer: %q", reply)
	}
}

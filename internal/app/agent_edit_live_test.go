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

// stageLive stages an edit through the receiver and hands the UI the change
// the way the receiver's notify does.
func stageLive(t *testing.T, m Model, receiver *ExternalReceiver, request string, started bool, policy string) Model {
	t.Helper()
	if reply := receiver.Handle(request); extmsg.IsErrorReply(reply) {
		t.Fatalf("staging failed: %q", reply)
	}
	m, cmd := deliver(m, agentEditsChangedMsg{session: "e1", started: started, policy: policy})
	return settleAgentEdits(m, cmd)
}

// endLive ends e1's unnamed agent and hands its end to the UI.
func endLive(t *testing.T, m Model, receiver *ExternalReceiver) (Model, chan string) {
	t.Helper()
	return sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", store.ExternalSettings{AgentEditPolicy: store.AgentEditPolicyReview, AgentEditKeep: 20})
}

func TestAnAgentSessionsFirstEditOpensItsLiveReview(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m = stageLive(t, m, receiver, replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}), true, store.AgentEditPolicyReview)
	if m.mode != modeReview || m.review.agentEdits != "e1" || !m.review.agentEditsLive || len(m.review.files) != 1 {
		t.Fatalf("the first edit opens the live review: mode=%v %q live=%v", m.mode, m.review.agentEdits, m.review.agentEditsLive)
	}
	if !strings.Contains(m.notice, "watching edit session e1 live") || !m.agentEditReviewLocked() {
		t.Fatalf("the user is told, and the decision is locked: %q", m.notice)
	}

	m = stageLive(t, m, receiver, "write --edit-session=e1\nlib/util.ts\n---\nexport const héllo = 2\n", false, store.AgentEditPolicyReview)
	if len(m.review.files) != 2 || m.review.fileByPath["lib/util.ts"] != 0 {
		t.Fatalf("a newly staged file joins in tree order: %+v", m.review.fileByPath)
	}
	if file, _ := m.selectedReviewFile(); file.workspaceRel != "lib/util.ts" || !file.loaded() || file.document.adds != 1 {
		t.Fatalf("the view follows the file edited last: %q loaded=%v", file.workspaceRel, file.loaded())
	}

	m = stageLive(t, m, receiver, replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "println(1)", New: "println(1)\n\tprintln(2)"}), false, store.AgentEditPolicyReview)
	file, _ := m.selectedReviewFile()
	if file.workspaceRel != "main.go" || file.document.adds != 2 || !strings.Contains(file.notes[0].overlay.text, "2 edits:") {
		t.Fatalf("a changed file reloads with its new edit log: %q +%d %+v", file.workspaceRel, file.document.adds, file.notes)
	}
}

func TestTheLiveReviewOpensOnlyForAFreeUserWhoReviewsFirst(t *testing.T) {
	request := "write --edit-session=e1\nmain.go\n---\nreplaced\n"
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyApply)
	if m = stageLive(t, m, receiver, request, true, store.AgentEditPolicyApply); m.mode == modeReview {
		t.Fatal("agent applies: nothing opens")
	}

	m, _, receiver, _, _ = agentEditsModel(t, store.AgentEditPolicyReview)
	next, cmd := m.Update(ctrlKey('t'))
	m = settleAgentEdits(next.(Model), cmd)
	if m = stageLive(t, m, receiver, request, true, store.AgentEditPolicyReview); m.mode != modeInspect {
		t.Fatalf("a user busy in another view is not moved: mode=%v", m.mode)
	}
	if header := headerLine(m); !strings.Contains(header, "✎ agent editing…") {
		t.Fatalf("the header label shows the session instead: %q", header)
	}
}

func TestTheLiveReviewUnlocksWhereTheUserIsOnceAgentsFinish(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m = stageLive(t, m, receiver, "write --edit-session=e1\nmain.go\n---\nreplaced\n", true, store.AgentEditPolicyReview)
	if status := ansi.Strip(m.renderStatusLine()); !strings.Contains(status, "agents are working… (editing · unnamed)") {
		t.Fatalf("the hint says the agent works: %q", status)
	}
	m, reply := endLive(t, m, receiver)
	if !strings.Contains(gotReply(t, reply), "the user is reviewing") {
		t.Fatal("the agent hears the user reviews")
	}
	if m.choice.open() || m.mode != modeReview || m.review.agentEdits != "e1" || m.agentEditReviewLocked() {
		t.Fatalf("no prompt: the review stays and unlocks: choice=%v mode=%v locked=%v", m.choice.open(), m.mode, m.agentEditReviewLocked())
	}
	if status := ansi.Strip(m.renderStatusLine()); !strings.Contains(status, "a apply all to disk · x discard all · r request changes") {
		t.Fatalf("the decision is offered: %q", status)
	}
}

func TestTheReviewComesBackWhenAgentsFinishWhileTheUserIsAway(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m = stageLive(t, m, receiver, "write --edit-session=e1\nmain.go\n---\nreplaced\n", true, store.AgentEditPolicyReview)
	m = key(m, keyPress(tea.KeyEscape))
	if m.mode == modeReview {
		t.Fatal("Esc leaves the locked review")
	}
	m, _ = endLive(t, m, receiver)
	if m.mode != modeReview || m.review.agentEdits != "e1" || m.choice.open() {
		t.Fatalf("the review comes back: mode=%v choice=%v", m.mode, m.choice.open())
	}
}

func TestRequestChangesAsksTheWaitingAgentAndKeepsTheReviewLive(t *testing.T) {
	m, root, waitReply := stagedReview(t)
	next, cmd := m.Update(typeRune('r'))
	m = settleAgentEdits(next.(Model), cmd)
	outcome := gotReply(t, waitReply)
	if !extmsg.IsChangesRequestedReply(outcome) || !strings.Contains(outcome, "switch to this terminal") {
		t.Fatalf("the waiting agent is told to ask the user: %q", outcome)
	}
	if m.mode != modeReview || !m.agentEditReviewLocked() || !strings.Contains(m.notice, "switch to the agent's terminal") {
		t.Fatalf("the review stays, locked while the agent works: mode=%v locked=%v notice=%q", m.mode, m.agentEditReviewLocked(), m.notice)
	}
	record, _ := m.agentEdits.registry.Snapshot("e1")
	if _, open := record.OpenChangeRequest(); !open || record.Phase() != store.PhaseEditing {
		t.Fatalf("the session is open for edits, the request logged: %+v", record.ChangeRequests)
	}
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" {
		t.Fatal("nothing is written")
	}

	// An agent that was not waiting hears it on its next wait.
	m, lateWait := sendAgentEditControl(t, m, m.agentEditsTestReceiver(t), extmsg.CommandWait, "e1", store.ExternalSettings{})
	if !extmsg.IsChangesRequestedReply(gotReply(t, lateWait)) {
		t.Fatal("a later wait is answered at once while the request is open")
	}
	if len(m.agentEdits.waiters["e1"]) != 0 {
		t.Fatal("and is not held")
	}
}

// agentEditsTestReceiver is a receiver over the model's registry, for the
// control messages sendAgentEditControl hands the UI (wait needs none of
// its state).
func (m Model) agentEditsTestReceiver(t *testing.T) *ExternalReceiver {
	t.Helper()
	return NewExternalReceiver(m.workspaceRoot, store.NewMemoryExternalSettings(store.ExternalSettings{Enabled: true}), store.NewMemoryExternalHistory()).WithAgentEdits(m.agentEdits.registry)
}

func TestRequestChangesFromTheLeaveChoice(t *testing.T) {
	m, _, waitReply := stagedReview(t)
	m = key(m, keyPress(tea.KeyEscape))
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyRight))
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Request changes
	m = settleAgentEdits(next.(Model), cmd)
	if !extmsg.IsChangesRequestedReply(gotReply(t, waitReply)) || m.mode != modeReview {
		t.Fatal("Request changes on the Esc prompt asks the agent and keeps the review")
	}
}

func TestTheHeaderLabelAnimatesAndOpensTheWorkingSession(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	next, cmd := withAgentEditsTick(m.Update(agentEditsChangedMsg{session: "e1"}))
	m = next.(Model)
	if !m.agentEdits.ticking || cmd == nil {
		t.Fatal("the label starts animating once an agent works")
	}
	before := headerLine(m)
	next, _ = m.Update(agentEditsTickMsg{})
	if after := headerLine(next.(Model)); after == before {
		t.Fatalf("a tick moves the spinner: %q", after)
	}
	m = next.(Model)

	header := headerLine(m)
	column := -1
	if at := strings.Index(header, "✎ agent editing"); at >= 0 {
		column = len([]rune(header[:at]))
	}
	if column < 0 || !m.headerAgentLabelHit(column) || m.headerAgentLabelHit(0) {
		t.Fatalf("the label is hit-tested where it is drawn: column %d", column)
	}
	next, cmd = m.Update(tea.MouseClickMsg{X: column, Y: 0, Button: tea.MouseLeft})
	m = settleAgentEdits(next.(Model), cmd)
	if m.mode != modeReview || m.review.agentEdits != "e1" {
		t.Fatalf("a click opens the working session's review: mode=%v %q", m.mode, m.review.agentEdits)
	}

	_, err := m.agentEdits.registry.Discard("e1", "the agent", 20)
	must(t, err)
	next, _ = m.Update(agentEditsChangedMsg{session: "e1"})
	next, cmd = next.(Model).Update(agentEditsTickMsg{})
	if next.(Model).agentEdits.ticking || cmd != nil {
		t.Fatal("the tick stops once no agent works")
	}
}

func TestAStagedFileOpensReadOnly(t *testing.T) {
	m, _, receiver, registry, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	next, _ := m.Update(agentEditsChangedMsg{session: "e1"})
	m = next.(Model).openFileAt("main.go")
	if _, locked := m.stagedReadOnly(); !locked {
		t.Fatal("a staged file is read-only")
	}
	if plain := ansi.Strip(m.renderStatusLine()); !strings.Contains(plain, "read-only · staged agent edits") {
		t.Fatalf("the status line says so: %q", plain)
	}
	original := m.edit.content()
	m = runes(m, "x ")
	m = key(m, keyPress(tea.KeyEnter))
	m = key(m, keyPress(tea.KeyBackspace))
	if m.edit.content() != original || m.edit.dirty || !strings.Contains(m.errText, "read-only — edit session e1 has staged edits") {
		t.Fatalf("typing changes nothing and says why: %q err=%q", m.edit.content(), m.errText)
	}
	next, _ = m.Update(tea.PasteMsg{Content: "pasted"})
	if next.(Model).edit.content() != original {
		t.Fatal("a paste changes nothing")
	}
	m = key(m, keyPress(tea.KeyDown))
	if m.edit.cy != 1 {
		t.Fatal("moving around still works")
	}

	_, err := registry.Discard("e1", "the agent", 20)
	must(t, err)
	next, _ = m.Update(agentEditsChangedMsg{session: "e1"})
	m = runes(next.(Model), "y")
	if !m.edit.dirty {
		t.Fatal("the file is editable again once nothing is staged")
	}
}

func TestUnsavedWorkInAFileGettingStagedStaysEditable(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m = runes(m.openFileAt("main.go"), "// mine ")
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	next, _ := m.Update(agentEditsChangedMsg{session: "e1"})
	m = runes(next.(Model), "more ")
	if !strings.HasPrefix(m.edit.content(), "// mine more ") {
		t.Fatalf("a dirty buffer keeps taking edits: %q", m.edit.content())
	}
}

func TestThePanelRequestsChangesOnAReadySession(t *testing.T) {
	m, _, waitReply := stagedReview(t)
	m = m.exitReview()
	next, cmd := m.Update(ctrlKey('t'))
	m = settleAgentEdits(next.(Model), cmd)
	for m.inspectMenu != inspectMenuAgentEdits {
		m = key(m, shiftKey(tea.KeyDown))
	}
	m = key(m, keyPress(tea.KeyRight))
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown)) // rows: policy, keep, e1
	next, cmd = m.Update(typeRune('r'))
	m = settleAgentEdits(next.(Model), cmd)
	if !extmsg.IsChangesRequestedReply(gotReply(t, waitReply)) {
		t.Fatal("r on a ready row asks its agents")
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "changes requested") {
		t.Fatalf("the row says changes were requested:\n%s", plain)
	}
}

func TestTheReviewWaitsForTheUserToStopTyping(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m.agentEdits.typingPause = time.Hour
	m = runes(m.openFileAt("main.go"), "ar")
	m = stageLive(t, m, receiver, "write --edit-session=e1\nlib/util.ts\n---\nx\n", true, store.AgentEditPolicyReview)
	if m.mode != modeEdit || !strings.HasPrefix(m.edit.content(), "ar") {
		t.Fatalf("a typing user stays in the editor: mode=%v", m.mode)
	}
	m, _ = endLive(t, m, receiver)
	if m.mode == modeReview || m.choice.kind != choiceAgentEditsReady {
		t.Fatalf("the end asks instead of moving them: mode=%v choice=%v", m.mode, m.choice.kind)
	}
}

func TestAChangeRequestReachesTheSenderAsItsOwnReply(t *testing.T) {
	line := replyFromOutcome(agentEditChangesRequestedReply("e1"))
	if !strings.HasPrefix(line, "ok changes requested — ") || strings.HasPrefix(line, "ok ok") || !extmsg.IsChangesRequestedReply(line) {
		t.Fatalf("the wire reply is not wrapped twice: %q", line)
	}
}

func TestALiveReviewRunsOneRefreshAtATime(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\none\n")
	m, prepare := m.enterAgentEditReview(mustSnapshotOf(t, m, "e1"), false)
	m = settleAgentEdits(m, prepare)

	receiver.Handle("write --edit-session=e1\nmain.go\n---\ntwo\n")
	m, first := m.refreshAgentEditReview("e1")
	if first == nil || !m.review.agentEditsRefreshing {
		t.Fatal("the first refresh runs")
	}
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nthree\n")
	m, second := m.refreshAgentEditReview("e1")
	if second != nil || !m.review.agentEditsRefreshDue {
		t.Fatal("a refresh asked for while one runs waits for it")
	}
	next, again := m.Update(first())
	m = next.(Model)
	if again == nil || !m.review.agentEditsRefreshing || m.review.agentEditsRefreshDue {
		t.Fatal("when it lands, one more runs for the edits since")
	}
	m = settleAgentEdits(m, again)
	if file, ok := m.selectedReviewFile(); !ok || file.staged.content != "three\n" {
		t.Fatalf("the review shows the latest staged content: %+v", file.staged)
	}
}

func mustSnapshotOf(t *testing.T, m Model, session string) store.AgentEditSession {
	t.Helper()
	record, ok := m.agentEdits.registry.Snapshot(session)
	if !ok {
		t.Fatalf("no session %s", session)
	}
	return record
}

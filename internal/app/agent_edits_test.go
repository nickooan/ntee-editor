package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/staging"
	"github.com/nickooan/ntee-editor/internal/store"
)

// agentEditsModel is a folder window taking external messages, with an
// agent edit registry wired into both the receiver and the model.
func agentEditsModel(t *testing.T, policy string) (Model, string, *ExternalReceiver, *staging.Registry, *store.MemoryExternalSettings) {
	t.Helper()
	m, root := newTestModel(t, nil)
	settings := store.NewMemoryExternalSettings(store.ExternalSettings{Enabled: true, HistoryLimit: 10, AgentEditPolicy: policy})
	history := store.NewMemoryExternalHistory()
	registry := staging.NewRegistry(root, store.NewMemoryAgentEditLog())
	receiver := NewExternalReceiver(root, settings, history).WithAgentEdits(registry)
	m = m.WithExternal(settings, history, "/tmp/ntee.sock", "").WithAgentEdits(registry)
	return m, root, receiver, registry, settings
}

// settleAgentEdits runs cmds and delivers what agent edits and their review
// produce, until nothing is left.
func settleAgentEdits(m Model, cmd tea.Cmd) Model {
	queue := execCmds(cmd)
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		switch msg.(type) {
		case agentEditsFinishedMsg, agentEditsFinishedListMsg, agentEditReviewPreparedMsg, reviewPreparedMsg, reviewLoadedMsg, externalSettingsMsg, externalHistoryLoadedMsg:
			var next tea.Cmd
			m, next = deliver(m, msg)
			queue = append(queue, execCmds(next)...)
		}
	}
	return m
}

// sendAgentEditControl delivers an edit session's end or wait to the UI as
// the receiver hands it over, and returns the channel its sender waits on.
func sendAgentEditControl(t *testing.T, m Model, receiver *ExternalReceiver, command, session string, settings store.ExternalSettings) (Model, chan string) {
	t.Helper()
	if command == extmsg.CommandEnd {
		if _, err := receiver.agentEdits.MarkAgentEnded(session); err != nil {
			t.Fatal(err)
		}
	}
	reply := make(chan string, 1)
	record := sessionRecord(command, session, "", "")
	m, cmd := deliver(m, externalMessageMsg{
		record: record, sessionKind: extmsg.SessionEdit, reply: reply,
		agentEdits: agentEditSettings{policy: settings.AgentEditPolicy, keep: settings.AgentEditKeep},
	})
	return settleAgentEdits(m, cmd), reply
}

func replaceRequest(session, path string, payload extmsg.ReplacePayload) string {
	return "replace --edit-session=" + session + "\n" + path + "\n---\n" + extmsg.EncodeReplace(payload)
}

func diskContent(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	must(t, err)
	return string(data)
}

func TestAgentEditReceiverStagesWithoutTouchingFiles(t *testing.T) {
	_, root, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)

	reply := receiver.Handle("write --edit-session=e1\n" + root + "/cmd/new.go\n---\npackage cmd\n")
	if !strings.HasPrefix(reply, "ok staged cmd/new.go (new file, 1 line) · edit 1 · 1 file staged · edit session e1 started") {
		t.Fatalf("write reply = %q", reply)
	}
	reply = receiver.Handle(replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	if reply != "ok replaced main.go L3–L5 · edit 1 · 2 files staged" {
		t.Fatalf("replace reply = %q", reply)
	}
	if reply = receiver.Handle(replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "nowhere", New: "x"})); !strings.HasPrefix(reply, "error the old text is not in main.go") {
		t.Fatalf("a missing old text is refused: %q", reply)
	}
	if reply = receiver.Handle(replaceRequest("e1", "../outside.go", extmsg.ReplacePayload{Old: "a", New: "b"})); !strings.HasPrefix(reply, "error path outside the project") {
		t.Fatalf("paths stay in the jail: %q", reply)
	}

	_, body, hasBody := extmsg.SplitBodyReply(receiver.Handle("show --edit-session=e1\nmain.go\n"))
	if !hasBody || body != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" {
		t.Fatalf("show prints the staged content: %q", body)
	}
	_, body, _ = extmsg.SplitBodyReply(receiver.Handle("show --edit-session=other\nmain.go\n"))
	if body != "package main\n\nfunc main() {\n}\n" {
		t.Fatalf("show of an unstaged file prints the disk: %q", body)
	}
	_, body, _ = extmsg.SplitBodyReply(receiver.Handle("status --edit-session=e1\n"))
	if !strings.Contains(body, "2 files staged · open for edits") || !strings.Contains(body, "main.go\tmodified · +1 −0 · 1 edit") || !strings.Contains(body, "cmd/new.go\tnew") {
		t.Fatalf("status = %q", body)
	}

	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" {
		t.Fatal("staging never writes the file")
	}
	if _, err := os.Stat(filepath.Join(root, "cmd")); !os.IsNotExist(err) {
		t.Fatal("a staged new file is not created")
	}

	if reply = receiver.Handle("discard --edit-session=e1\n"); !strings.HasPrefix(reply, "ok discarded edit session e1 — 2 staged files dropped") {
		t.Fatalf("discard reply = %q", reply)
	}
	if reply = receiver.Handle("write --edit-session=e1\nmain.go\n---\nx"); !strings.Contains(reply, "already finished (discarded)") {
		t.Fatalf("a discarded session takes no more edits: %q", reply)
	}
}

func TestAgentEditEndAppliesAtOnceIntoTheOpenBuffer(t *testing.T) {
	m, root, receiver, _, settings := agentEditsModel(t, store.AgentEditPolicyApply)
	m = m.openFileAt("main.go")
	must(t, os.WriteFile(filepath.Join(root, "other.go"), []byte("package main\n"), 0o644))
	receiver.Handle(replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	receiver.Handle("write --edit-session=e1\nother.go\n---\npackage main\n\nvar x = 1\n")

	current, _ := settings.LoadExternalSettings()
	m, reply := sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	if outcome := gotReply(t, reply); outcome != "finished — edit session e1 applied 2 files" {
		t.Fatalf("end reply = %q", outcome)
	}
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" || diskContent(t, root, "other.go") != "package main\n\nvar x = 1\n" {
		t.Fatal("every staged file is written")
	}
	if m.edit.dirty || m.edit.content() != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" {
		t.Fatalf("the open buffer holds the applied content, saved: dirty=%v %q", m.edit.dirty, m.edit.content())
	}
	m = m.undo()
	if m.edit.content() != "package main\n\nfunc main() {\n}\n" {
		t.Fatalf("one undo step goes back to before the agent's edit: %q", m.edit.content())
	}
	m, waitReply := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	if outcome := gotReply(t, waitReply); !strings.HasPrefix(outcome, "finished — edit session e1 applied 2 files") {
		t.Fatalf("a late wait hears the outcome: %q", outcome)
	}
	_ = m
}

func TestAgentEditApplySkipsUnsavedWorkAndConflicts(t *testing.T) {
	m, root, receiver, _, settings := agentEditsModel(t, store.AgentEditPolicyApply)
	m = m.openFileAt("main.go")
	m = runes(m, "// mine ")
	must(t, os.WriteFile(filepath.Join(root, "drift.go"), []byte("package main\n"), 0o644))
	receiver.Handle(replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "package main", New: "package app"}))
	receiver.Handle("write --edit-session=e1\ndrift.go\n---\npackage main // agent\n")
	must(t, os.WriteFile(filepath.Join(root, "drift.go"), []byte("package main // someone else\n"), 0o644))

	current, _ := settings.LoadExternalSettings()
	m, reply := sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	outcome := gotReply(t, reply)
	if !strings.HasPrefix(outcome, "error edit session e1 applied no file") ||
		!strings.Contains(outcome, "main.go (unsaved edits in the editor)") || !strings.Contains(outcome, "drift.go (changed on disk since it was staged)") {
		t.Fatalf("end reply = %q", outcome)
	}
	if !m.edit.dirty || !strings.HasPrefix(m.edit.content(), "// mine package main") {
		t.Fatalf("the user's unsaved edits are untouched: %q", m.edit.content())
	}
	if diskContent(t, root, "drift.go") != "package main // someone else\n" || diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" {
		t.Fatal("nothing is written over unsaved or newer work")
	}
}

func TestAgentEditReviewThenApply(t *testing.T) {
	m, root, receiver, _, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle(replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	current, _ := settings.LoadExternalSettings()

	m, reply := sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	if outcome := gotReply(t, reply); !strings.HasPrefix(outcome, "edit session e1 ended — the user is reviewing 1 staged file") {
		t.Fatalf("end reply = %q", outcome)
	}
	if m.choice.kind != choiceAgentEditsReady || m.choice.title != "EDITS · agent edit session e1" ||
		!strings.Contains(m.choice.body[0].text, "NOT written yet") || m.choice.choices[0] != "Review edits" {
		t.Fatalf("the user is offered the review, told the edits are not written: %+v", m.choice)
	}
	m, waitReply := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	noReply(t, waitReply)

	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Review edits
	m = settleAgentEdits(next.(Model), cmd)
	if m.mode != modeReview || m.review.agentEdits != "e1" || len(m.review.files) != 1 {
		t.Fatalf("the staged review is open: mode=%v review=%+v", m.mode, m.review.agentEdits)
	}
	file, _ := m.selectedReviewFile()
	if file.document.adds != 1 || file.document.dels != 0 || len(file.notes) != 1 || !strings.Contains(file.notes[0].overlay.text, "replace L3–L5") {
		t.Fatalf("diff before → staged with the edit log: +%d −%d notes=%+v", file.document.adds, file.document.dels, file.notes)
	}
	plain := ansi.Strip(m.View().Content)
	decision, navigation := strings.Index(plain, "a apply all to disk · x discard all · Esc decide"), strings.Index(plain, "↑/↓ move")
	if !strings.Contains(plain, "edit session e1 · staged, not written") || decision < 0 || navigation < decision {
		t.Fatalf("the decision leads the status bar:\n%s", plain)
	}

	m = key(m, typeRune('A'))
	if m.choice.kind != choiceApplyAgentEdits {
		t.Fatal("A asks before applying")
	}
	next, cmd = m.Update(keyPress(tea.KeyEnter))
	m = settleAgentEdits(next.(Model), cmd)
	if outcome := gotReply(t, waitReply); outcome != "finished — edit session e1 applied 1 file" {
		t.Fatalf("the waiting agent hears the outcome: %q", outcome)
	}
	if m.mode == modeReview {
		t.Fatal("the review closes once applied")
	}
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" {
		t.Fatal("applied to disk")
	}
}

func TestAgentEditReviewDiscard(t *testing.T) {
	m, root, receiver, registry, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	current, _ := settings.LoadExternalSettings()
	m, _ = sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	m = key(m, typeRune('n')) // Later
	if m.choice.open() || m.mode == modeReview {
		t.Fatal("Later leaves the session staged")
	}
	m, waitReply := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)

	record, _ := registry.Snapshot("e1")
	m, cmd := m.enterAgentEditReview(record, false)
	m = settleAgentEdits(m, cmd)
	m = key(m, typeRune('X'))
	if m.choice.kind != choiceDiscardAgentEdits {
		t.Fatal("X asks before discarding")
	}
	next, cmd := m.Update(typeRune('y'))
	m = settleAgentEdits(next.(Model), cmd)
	if outcome := gotReply(t, waitReply); outcome != extmsg.EndedReply("edit session e1 was discarded by the user — nothing was written") {
		t.Fatalf("the agent hears the discard: %q", outcome)
	}
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" {
		t.Fatal("discarding writes nothing")
	}
	if len(m.agentEdits.finished) != 1 || m.agentEdits.finished[0].Status != store.AgentEditDiscarded {
		t.Fatalf("the discarded session stays for tracing: %+v", m.agentEdits.finished)
	}
}

func TestAgentEditsPanelPolicyAndTrace(t *testing.T) {
	m, _, receiver, registry, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=done1\nmain.go\n---\nreplaced\n")
	_, err := registry.Discard("done1", "the agent", 20)
	must(t, err)
	receiver.Handle("write --edit-session=open1\nmain.go\n---\nstaged\n")

	next, cmd := m.Update(ctrlKey('t'))
	m = settleAgentEdits(next.(Model), cmd)
	for m.inspectMenu != inspectMenuAgentEdits {
		m = key(m, shiftKey(tea.KeyDown))
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"review first", "open1", "● staging", "done1", "discarded"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("panel missing %q:\n%s", want, plain)
		}
	}

	m = key(m, keyPress(tea.KeyRight))
	next, cmd = m.Update(keyPress(tea.KeyEnter)) // toggle the policy
	m = settleAgentEdits(next.(Model), cmd)
	if got, _ := settings.LoadExternalSettings(); got.AgentEditPolicy != store.AgentEditPolicyApply || m.external.settings.AgentEditPolicy != store.AgentEditPolicyApply {
		t.Fatal("Enter on the policy row switches to agent applies")
	}

	// Rows: policy, keep, open1 (pending), done1 (finished).
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown))
	next, cmd = m.Update(keyPress(tea.KeyEnter))
	m = settleAgentEdits(next.(Model), cmd)
	if m.mode != modeReview || m.review.agentEdits != "done1" || !m.review.agentEditsTrace {
		t.Fatalf("a finished session opens as a trace: mode=%v %q trace=%v", m.mode, m.review.agentEdits, m.review.agentEditsTrace)
	}
	file, _ := m.selectedReviewFile()
	if len(file.notes) != 1 || !strings.Contains(file.notes[0].overlay.text, "1 edit:") {
		t.Fatalf("the trace shows the edit log: %+v", file.notes)
	}
	m = key(m, typeRune('A'))
	if m.choice.open() {
		t.Fatal("a trace is read-only")
	}
}

func TestAgentEditUserApplyCanReplaceUnsavedWork(t *testing.T) {
	m, root, receiver, _, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	m = m.openFileAt("main.go")
	m = runes(m, "// mine ")
	receiver.Handle(replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "package main", New: "package app"}))
	current, _ := settings.LoadExternalSettings()
	m, _ = sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	m.choice = choiceOverlay{}

	m = m.askApplyAgentEdits("e1")
	if len(m.choice.choices) != 3 || !strings.Contains(m.choice.body[len(m.choice.body)-1].text, "unsaved edits in main.go") {
		t.Fatalf("unsaved work gets its own choice: %+v", m.choice)
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Apply, replace unsaved
	m = settleAgentEdits(next.(Model), cmd)
	if m.edit.dirty || diskContent(t, root, "main.go") != "package app\n\nfunc main() {\n}\n" {
		t.Fatalf("applied over the unsaved edits: dirty=%v", m.edit.dirty)
	}
	m = m.undo()
	if !strings.HasPrefix(m.edit.content(), "// mine package main") {
		t.Fatalf("undo brings the unsaved edits back: %q", m.edit.content())
	}
}

// stagedReview stages one edit, ends the session, opens its review, and
// returns the wait reply channel of an agent waiting on it.
func stagedReview(t *testing.T) (Model, string, chan string) {
	t.Helper()
	return stagedReviewWith(t, func(m Model) Model { return m })
}

// stagedReviewWith is stagedReview with prepare run on the model first.
func stagedReviewWith(t *testing.T, prepare func(Model) Model) (Model, string, chan string) {
	t.Helper()
	m, root, receiver, _, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	m = prepare(m)
	receiver.Handle(replaceRequest("e1", "main.go", extmsg.ReplacePayload{Old: "func main() {\n}", New: "func main() {\n\tprintln(1)\n}"}))
	current, _ := settings.LoadExternalSettings()
	m, _ = sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	m, waitReply := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Review edits
	m = settleAgentEdits(next.(Model), cmd)
	if m.mode != modeReview || m.review.agentEdits != "e1" {
		t.Fatal("the staged review is open")
	}
	return m, root, waitReply
}

func TestAgentEditReviewKeysTakeEitherCase(t *testing.T) {
	m, _, _ := stagedReview(t)
	for _, press := range []struct {
		key  rune
		want choiceOverlayKind
	}{{'a', choiceApplyAgentEdits}, {'A', choiceApplyAgentEdits}, {'x', choiceDiscardAgentEdits}, {'X', choiceDiscardAgentEdits}} {
		asked := key(m, typeRune(press.key))
		if asked.choice.kind != press.want {
			t.Fatalf("%q opened choice %v, want %v", press.key, asked.choice.kind, press.want)
		}
	}
	if asked, _ := m.handleAgentEditReviewKey("shift+a"); asked.choice.kind != choiceApplyAgentEdits {
		t.Fatal("shift+a reported without text applies too")
	}
}

func TestAgentEditReviewEscKeepsForLaterAndTellsTheAgent(t *testing.T) {
	m, root, waitReply := stagedReview(t)
	m = key(m, keyPress(tea.KeyEscape))
	if m.choice.kind != choiceLeaveAgentEditReview || strings.Join(m.choice.choices, "|") != "Apply all|Discard all|Keep for later" {
		t.Fatalf("Esc asks what to do: %+v", m.choice)
	}
	if m.mode != modeReview {
		t.Fatal("the review stays open while the user decides")
	}
	noReply(t, waitReply)
	m = key(m, keyPress(tea.KeyEscape)) // Esc again: keep for later
	if m.mode == modeReview || m.choice.open() {
		t.Fatal("keep for later leaves the review")
	}
	if outcome := gotReply(t, waitReply); outcome != agentEditDeferredReply("e1") || !strings.Contains(outcome, "aside for later") {
		t.Fatalf("the waiting agent hears the edits were set aside: %q", outcome)
	}
	if _, pending := m.agentEdits.registry.Snapshot("e1"); !pending || len(m.agentEdits.waiters["e1"]) != 0 {
		t.Fatal("the session stays staged, with no waiter left behind")
	}
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" {
		t.Fatal("keep for later writes nothing")
	}
}

func TestAgentEditReviewEscApplyAll(t *testing.T) {
	m, root, waitReply := stagedReview(t)
	m = key(m, keyPress(tea.KeyEscape))
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Apply all
	m = settleAgentEdits(next.(Model), cmd)
	if outcome := gotReply(t, waitReply); outcome != "finished — edit session e1 applied 1 file" {
		t.Fatalf("apply all from the Esc prompt applies at once: %q", outcome)
	}
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" || m.mode == modeReview {
		t.Fatal("applied to disk and the review closed")
	}
}

func TestAgentEditReviewEscDiscardAll(t *testing.T) {
	m, root, waitReply := stagedReview(t)
	m = key(m, keyPress(tea.KeyEscape))
	m = key(m, keyPress(tea.KeyRight))
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Discard all
	m = settleAgentEdits(next.(Model), cmd)
	if outcome := gotReply(t, waitReply); !extmsg.IsEndedReply(outcome) || !strings.Contains(outcome, "discarded by the user") {
		t.Fatalf("discard all tells the agent: %q", outcome)
	}
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" || m.mode == modeReview {
		t.Fatal("nothing written and the review closed")
	}
}

func TestAgentEditReviewEscApplyAllAsksAboutUnsavedWork(t *testing.T) {
	m, root, waitReply := stagedReviewWith(t, func(m Model) Model { return runes(m.openFileAt("main.go"), "// mine ") })
	m = key(m, keyPress(tea.KeyEscape))
	m = key(m, keyPress(tea.KeyEnter)) // Apply all
	if m.choice.kind != choiceApplyAgentEdits || len(m.choice.choices) != 3 {
		t.Fatalf("unsaved work is asked about first: %+v", m.choice)
	}
	noReply(t, waitReply)
	if diskContent(t, root, "main.go") != "package main\n\nfunc main() {\n}\n" {
		t.Fatal("nothing is written before the user answers")
	}
}

func TestAgentEditReadyLaterTellsTheWaitingAgent(t *testing.T) {
	m, _, receiver, _, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	current, _ := settings.LoadExternalSettings()
	m, _ = sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	m, waitReply := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	m = key(m, typeRune('n')) // Later
	if outcome := gotReply(t, waitReply); !strings.Contains(outcome, "aside for later") {
		t.Fatalf("Later on the ready prompt answers the agent: %q", outcome)
	}
}

func TestAgentEditTraceEscJustLeaves(t *testing.T) {
	m, _, receiver, registry, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=done1\nmain.go\n---\nreplaced\n")
	record, err := registry.Discard("done1", "the agent", 20)
	must(t, err)
	m, cmd := m.enterAgentEditReview(record, true)
	m = settleAgentEdits(m, cmd)
	if plain := ansi.Strip(m.View().Content); strings.Contains(plain, "a apply all to disk") {
		t.Fatal("a trace offers no decision")
	}
	m = key(m, keyPress(tea.KeyEscape))
	if m.mode == modeReview || m.choice.open() {
		t.Fatal("Esc leaves a trace at once")
	}
}

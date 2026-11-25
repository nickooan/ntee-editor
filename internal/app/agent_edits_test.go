package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	m.agentEdits.typingPause = 0 // tests type and receive at once
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
		case agentEditsFinishedMsg, agentEditsForceEndedMsg, agentEditsFinishedListMsg, agentEditReviewPreparedMsg, agentEditReviewRefreshedMsg, agentEditsChangesRequestedMsg, reviewPreparedMsg, reviewLoadedMsg, externalSettingsMsg, externalHistoryLoadedMsg:
			var next tea.Cmd
			m, next = deliver(m, msg)
			queue = append(queue, execCmds(next)...)
		}
	}
	return m
}

// sendAgentEditControl delivers an edit session's end or wait to the UI as
// the receiver hands it over, and returns the channel its sender waits on.
// An end stands for the session becoming the user's: its agent ends and
// checks it (readyAgentEdits).
func sendAgentEditControl(t *testing.T, m Model, receiver *ExternalReceiver, command, session string, settings store.ExternalSettings) (Model, chan string) {
	t.Helper()
	if command == extmsg.CommandEnd {
		readyAgentEdits(t, receiver, session, "")
	}
	reply := make(chan string, 1)
	record := sessionRecord(command, session, "", "")
	m, cmd := deliver(m, externalMessageMsg{
		record: record, sessionKind: extmsg.SessionEdit, reply: reply,
		agentEdits: agentEditSettings{policy: settings.AgentEditPolicy, keep: settings.AgentEditKeep},
	})
	return settleAgentEdits(m, cmd), reply
}

// readyAgentEdits ends the agents' parts and records a passing check from
// each, so the session is the user's to review.
func readyAgentEdits(t *testing.T, receiver *ExternalReceiver, session string, agents ...string) {
	t.Helper()
	for _, agent := range agents {
		if _, _, err := receiver.agentEdits.MarkAgentEnded(session, agent); err != nil {
			t.Fatal(err)
		}
	}
	for _, agent := range agents {
		if _, _, err := receiver.agentEdits.RecordCheck(session, agent, store.CheckPassed, "tests ok"); err != nil {
			t.Fatal(err)
		}
	}
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
	if reply != "ok replaced main.go L3–L5 · edit 1 · 2 files staged · version "+staging.Version("package main\n\nfunc main() {\n\tprintln(1)\n}\n") {
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
	if body != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" {
		t.Fatalf("another session's show prints the latest content, e1's staged copy: %q", body)
	}
	line, body, _ := extmsg.SplitBodyReply(receiver.Handle("read\nmain.go\n"))
	if extmsg.BodyDetail(line) != "staged · edit session e1 · not written · version "+staging.Version(body) || body != "package main\n\nfunc main() {\n\tprintln(1)\n}\n" {
		t.Fatalf("read without a session gets the latest content and says where from: %q %q", line, body)
	}
	if line, body, _ = extmsg.SplitBodyReply(receiver.Handle("read\nlib/util.ts\n")); extmsg.BodyDetail(line) != "disk · version "+staging.Version(body) || body != "export const héllo = 1\n" {
		t.Fatalf("an unstaged file reads from disk: %q %q", line, body)
	}
	if reply := receiver.Handle("staged\nmain.go\n"); reply != "ok staged · edit session e1" {
		t.Fatalf("staged = %q", reply)
	}
	if reply := receiver.Handle("staged\nlib/util.ts\n"); reply != "ok not staged" {
		t.Fatalf("staged of an unstaged file = %q", reply)
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
	if m.choice.open() || m.mode != modeReview || m.review.agentEdits != "e1" || len(m.review.files) != 1 {
		t.Fatalf("from the plain view the review opens at once, no prompt: choice=%+v mode=%v review=%q", m.choice, m.mode, m.review.agentEdits)
	}
	if !strings.HasPrefix(m.notice, "✎ agents finished") {
		t.Fatalf("the notice says the agents finished: %q", m.notice)
	}
	m, waitReply := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	noReply(t, waitReply)
	file, _ := m.selectedReviewFile()
	if file.document.adds != 1 || file.document.dels != 0 || len(file.notes) != 1 || !strings.Contains(file.notes[0].overlay.text, "replace L3–L5") {
		t.Fatalf("diff before → staged with the edit log: +%d −%d notes=%+v", file.document.adds, file.document.dels, file.notes)
	}
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "edit session e1 · staged, not written") || !strings.Contains(plain, "╭─ ⏳ agent waiting for you") || !strings.Contains(plain, "discard all") ||
		strings.Contains(ansi.Strip(m.renderStatusLine()), "a apply all to disk") {
		t.Fatalf("the decision floats over the code, not in the status bar:\n%s", plain)
	}

	m = key(m, typeRune('A'))
	if m.choice.kind != choiceApplyAgentEdits {
		t.Fatal("A asks before applying")
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter))
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
	m, waitReply := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	if _, pending := registry.Snapshot("e1"); !pending || m.mode != modeReview {
		t.Fatal("the ended session is staged, its review open")
	}
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
	if m.choice.kind != choiceAgentEditsUnsaved || len(m.choice.choices) != 3 || !strings.Contains(m.choice.body[0].text, "unsaved edits in main.go") {
		t.Fatalf("unsaved work is its own first step: %+v", m.choice)
	}
	m = key(m, keyPress(tea.KeyEnter)) // Replace unsaved
	if m.choice.kind != choiceApplyAgentEdits || !strings.Contains(m.choice.body[len(m.choice.body)-1].text, "unsaved edits in main.go — replaced") {
		t.Fatalf("then the apply popup, saying what becomes of it: %+v", m.choice)
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Apply
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
	if m.mode != modeReview || m.review.agentEdits != "e1" {
		t.Fatal("the staged review is open")
	}
	return m, root, waitReply
}

func TestASessionSpreadingIntoAnotherRepoIsAnnouncedAndTheReviewJumpsBetweenRepos(t *testing.T) {
	m, root, receiver, _, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	must(t, os.MkdirAll(filepath.Join(root, "app"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "app", "a.go"), []byte("package app\n"), 0o644))
	m.workspaceRepos = []string{"app", "lib"}

	receiver.Handle("write --edit-session=e1\napp/a.go\n---\npackage app // one\n")
	m, _ = deliver(m, agentEditsChangedMsg{session: "e1"})
	if strings.Contains(m.notice, "now also in") {
		t.Fatalf("the first repo is no spread: %q", m.notice)
	}
	receiver.Handle("write --edit-session=e1\napp/b.go\n---\npackage app // two\n")
	m, _ = deliver(m, agentEditsChangedMsg{session: "e1"})
	if strings.Contains(m.notice, "now also in") {
		t.Fatalf("another file in the same repo is no spread: %q", m.notice)
	}
	receiver.Handle("write --edit-session=e1\nlib/util.ts\n---\nexport const two = 2\n")
	m, _ = deliver(m, agentEditsChangedMsg{session: "e1"})
	if m.notice != "✎ edit session e1 · now also in lib" {
		t.Fatalf("spreading into lib is announced: %q", m.notice)
	}

	current, _ := settings.LoadExternalSettings()
	m, _ = sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	m, _ = sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	if m.mode != modeReview || len(m.review.files) != 3 {
		t.Fatalf("the review holds every repo's files: mode %v, %d files", m.mode, len(m.review.files))
	}
	if repos := m.reviewRepos(); strings.Join(repos, ",") != "app,lib" {
		t.Fatalf("repos = %v", repos)
	}
	if !strings.Contains(m.renderReviewStatus(), "{/} repo") {
		t.Fatalf("the hint offers the repo jump: %q", m.renderReviewStatus())
	}
	selectedRepo := func(m Model) string {
		file, _ := m.selectedReviewFile()
		return m.reviewFileRepo(file)
	}
	m, _ = m.selectReviewFile(slices.IndexFunc(m.review.files, func(file reviewFile) bool { return file.workspaceRel == "app/b.go" }))
	if m = key(m, typeRune('}')); selectedRepo(m) != "lib" {
		t.Fatalf("} goes to the next repo: %q", selectedRepo(m))
	}
	if m = key(m, typeRune('}')); selectedRepo(m) != "lib" {
		t.Fatalf("} at the last repo stays: %q", selectedRepo(m))
	}
	m = key(m, typeRune('{'))
	if file, _ := m.selectedReviewFile(); file.workspaceRel != "app/a.go" {
		t.Fatalf("{ goes to the previous repo's first file: %q", file.workspaceRel)
	}
	next, _ := m.handleReviewKey(tea.KeyPressMsg{Code: ']', Mod: tea.ModShift})
	if m = next.(Model); selectedRepo(m) != "lib" {
		t.Fatalf("shift+] reported without text jumps too: %q", selectedRepo(m))
	}
	next, _ = m.handleReviewKey(tea.KeyPressMsg{Code: '[', Mod: tea.ModShift})
	if m = next.(Model); selectedRepo(m) != "app" {
		t.Fatalf("shift+[ reported without text jumps back: %q", selectedRepo(m))
	}
}

func TestTheRepoJumpIsOfferedOnlyWithSeveralRepos(t *testing.T) {
	m, _, _ := stagedReview(t)
	if strings.Contains(m.renderReviewStatus(), "{/} repo") {
		t.Fatal("one repo: no repo jump hint")
	}
	before, _ := m.selectedReviewFile()
	m = key(m, typeRune('}'))
	if after, _ := m.selectedReviewFile(); after.workspaceRel != before.workspaceRel {
		t.Fatal("} with one repo does nothing")
	}
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
	if asked, _, _ := m.handleAgentEditReviewKey("shift+a"); asked.choice.kind != choiceApplyAgentEdits {
		t.Fatal("shift+a reported without text applies too")
	}
}

func TestAgentEditReviewEscKeepsForLaterAndTellsTheAgent(t *testing.T) {
	m, root, waitReply := stagedReview(t)
	m = key(m, keyPress(tea.KeyEscape))
	if m.choice.kind != choiceLeaveAgentEditReview || strings.Join(m.choice.choices, "|") != "Apply all|Discard all|Request changes|Keep for later" {
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
	m = key(m, keyPress(tea.KeyEnter)) // Apply all
	if m.choice.kind != choiceApplyAgentEdits {
		t.Fatalf("Apply all opens the apply popup: %+v", m.choice)
	}
	noReply(t, waitReply)
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Apply
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
	if m.choice.kind != choiceAgentEditsUnsaved || len(m.choice.choices) != 3 {
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
	next, cmd := m.Update(ctrlKey('t')) // busy elsewhere: the end asks instead of moving the user
	m = settleAgentEdits(next.(Model), cmd)
	m, _ = sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	if m.choice.kind != choiceAgentEditsReady || m.choice.title != "EDITS · agent edit session e1" ||
		!strings.Contains(m.choice.body[0].text, "NOT written yet") || m.choice.choices[0] != "Review edits" {
		t.Fatalf("a busy user is offered the review, told the edits are not written: %+v", m.choice)
	}
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

func TestAgentEditApplyKeepsADraftItCouldNotReplace(t *testing.T) {
	m, root, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\ndrift.go\n---\npackage main // agent\n")
	must(t, m.db.SaveDraft(store.Draft{Path: "drift.go", Content: "package main // my draft\n"}))
	m.draftSet["drift.go"] = true
	must(t, os.WriteFile(filepath.Join(root, "drift.go"), []byte("package main // someone else\n"), 0o644))
	_, _, err := receiver.agentEdits.MarkAgentEnded("e1", "")
	must(t, err)

	m, cmd := m.startAgentEditApply("e1", store.AgentEditPolicyReview, true, store.GitFollowUpNone, 10, nil)
	m = settleAgentEdits(m, cmd)
	if _, ok := m.db.LoadDraft("drift.go"); !ok || !m.draftSet["drift.go"] {
		t.Fatal("the apply was refused (drift on disk), so the draft it would have replaced stays")
	}
}

func TestAgentEditApplyDropsADraftItReplaced(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nfresh.go\n---\npackage main // agent\n")
	must(t, m.db.SaveDraft(store.Draft{Path: "fresh.go", Content: "package main // my draft\n"}))
	m.draftSet["fresh.go"] = true
	_, _, err := receiver.agentEdits.MarkAgentEnded("e1", "")
	must(t, err)

	m, cmd := m.startAgentEditApply("e1", store.AgentEditPolicyReview, true, store.GitFollowUpNone, 10, nil)
	m = settleAgentEdits(m, cmd)
	if _, ok := m.db.LoadDraft("fresh.go"); ok || m.draftSet["fresh.go"] {
		t.Fatal("the written file replaced the draft")
	}
}

func TestAgentEditNoticesReachTheUIInOrderAndCoalesced(t *testing.T) {
	_, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	release := make(chan struct{})
	delivered := make(chan any, 16)
	receiver.SetSink(func(msg any) {
		if _, isRequest := msg.(externalMessageMsg); isRequest {
			return
		}
		<-release // a busy UI
		delivered <- msg
	})
	receiver.notify(agentEditsChangedMsg{session: "a"})
	time.Sleep(20 * time.Millisecond) // the dispatcher holds the first notice
	receiver.notify(agentEditsChangedMsg{session: "b"})
	receiver.notify(agentEditsChangedMsg{session: "b"})
	receiver.notify(agentEditsReopenedMsg{session: "a", agent: "x"})
	close(release)
	var sessions []string
	timeout := time.After(2 * time.Second)
	for len(sessions) < 3 {
		select {
		case msg := <-delivered:
			switch msg := msg.(type) {
			case agentEditsChangedMsg:
				sessions = append(sessions, "changed:"+msg.session)
			case agentEditsReopenedMsg:
				sessions = append(sessions, "reopened:"+msg.session)
			}
		case <-timeout:
			t.Fatalf("notices delivered: %v", sessions)
		}
	}
	if got := strings.Join(sessions, ","); got != "changed:a,changed:b,reopened:a" {
		t.Fatalf("notices arrive in order, a repeated change once: %s", got)
	}
	select {
	case extra := <-delivered:
		t.Fatalf("no extra notice: %+v", extra)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAnExpiredAgentEditWaitIsForgotten(t *testing.T) {
	m, _, receiver, _, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	current, _ := settings.LoadExternalSettings()
	m, _ = sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	m, first := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	m, second := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	if len(m.agentEdits.waiters["e1"]) != 2 {
		t.Fatalf("two waits are held: %d", len(m.agentEdits.waiters["e1"]))
	}
	m, _ = deliver(m, sessionWaitExpiredMsg{reply: first})
	if waiters := m.agentEdits.waiters["e1"]; len(waiters) != 1 || waiters[0] != (chan<- string)(second) {
		t.Fatalf("the expired wait is dropped, the live one kept: %v", waiters)
	}
	m, _ = deliver(m, sessionWaitExpiredMsg{reply: second})
	if _, held := m.agentEdits.waiters["e1"]; held {
		t.Fatal("no wait left for the session")
	}
}

func TestTheAgentEditLabelStopsAnimatingForAQuietSession(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	m = m.refreshAgentEditsPending()
	if !m.agentsAnimating() {
		t.Fatal("an agent that just edited animates the label")
	}
	m.agentEdits.lastActivityAt = time.Now().Add(-agentEditsAnimationIdle)
	if !m.agentsWorking() || m.agentsAnimating() {
		t.Fatal("a quiet session still shows as editing, without the animation")
	}
	m.agentEdits.ticking = true
	next, cmd := m.handleAgentEditsTick()
	if cmd != nil || next.(Model).agentEdits.ticking {
		t.Fatal("the tick chain stops")
	}
	if label := next.(Model).headerAgentEditingLabel(); !strings.HasPrefix(label, "· ✎ agent editing… · idle 10m") {
		t.Fatalf("a quiet session shows a still mark and how long it was quiet, not a frozen frame: %q", label)
	}
}

func TestAnAgentCallThatChangesNothingKeepsTheLabelAnimating(t *testing.T) {
	m, _, receiver, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	m = m.refreshAgentEditsPending()
	m.agentEdits.lastActivityAt = time.Now().Add(-agentEditsAnimationIdle)
	m, cmd := deliver(m, agentEditsSeenMsg{session: "e1"})
	if !m.agentsAnimating() || !m.agentEdits.ticking || cmd == nil {
		t.Fatal("a read or sandbox call from the session's agent restarts the animation")
	}
	if m = m.refreshAgentEditsPending(); !m.agentsAnimating() {
		t.Fatal("the heartbeat survives a refresh from the registry")
	}
	if strings.Contains(m.headerAgentEditingLabel(), "idle") {
		t.Fatalf("an agent seen just now is not idle: %q", m.headerAgentEditingLabel())
	}
}

func TestAHeartbeatNoticeIsQueuedOncePerSession(t *testing.T) {
	receiver := &ExternalReceiver{}
	receiver.notices = []any{agentEditsSeenMsg{session: "e1"}}
	if !receiver.noticeAlreadyWaiting(agentEditsSeenMsg{session: "e1"}) {
		t.Fatal("a second heartbeat for the same session is covered by the waiting one")
	}
	if receiver.noticeAlreadyWaiting(agentEditsSeenMsg{session: "e2"}) {
		t.Fatal("another session's heartbeat is queued")
	}
}

func TestAnAgentBlockedInWaitShowsAsWaitingForReview(t *testing.T) {
	m, _, receiver, _, settings := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nreplaced\n")
	current, _ := settings.LoadExternalSettings()
	m, _ = sendAgentEditControl(t, m, receiver, extmsg.CommandEnd, "e1", current)
	m = m.refreshAgentEditsPending()
	if len(m.sessionsAwaitingReview()) != 0 || m.headerAgentEditingLabel() != "" {
		t.Fatalf("a ready session nobody waits on shows nothing: %q", m.headerAgentEditingLabel())
	}
	m, wait := sendAgentEditControl(t, m, receiver, extmsg.CommandWait, "e1", current)
	if got := m.sessionsAwaitingReview(); len(got) != 1 || got[0] != "e1" {
		t.Fatalf("the waited-on session is awaiting review: %v", got)
	}
	if !m.agentsAnimating() || !m.agentEdits.ticking {
		t.Fatal("a waiting agent animates the label")
	}
	if label := m.headerAgentEditingLabel(); !strings.HasSuffix(label, " ⏳ agent waiting for your review") || strings.HasPrefix(label, "·") {
		t.Fatalf("header = %q", label)
	}
	m.mode, m.review.agentEdits = modeReview, "e1"
	if hint := m.agentEditReviewHint(); !strings.Contains(hint, "⏳ agent waiting for you") || !strings.Contains(hint, "a apply all to disk") {
		t.Fatalf("the review hint says the agent waits and keeps the decision keys: %q", hint)
	}

	m, _ = deliver(m, sessionWaitExpiredMsg{reply: wait})
	if len(m.sessionsAwaitingReview()) != 1 {
		t.Fatal("between one wait giving up and the next, the session still shows as awaited")
	}
	m.agentEdits.waitExpiredAt = map[string]time.Time{"e1": time.Now().Add(-agentEditWaitGrace)}
	if len(m.sessionsAwaitingReview()) != 0 || m.agentsAnimating() {
		t.Fatal("once the grace passes with no new wait, the agent is no longer counted as waiting")
	}
}

func TestAStaleFinishedListLoadIsDropped(t *testing.T) {
	m, _, _, _, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	m, _ = m.loadAgentEditsFinishedCmd()
	older := m.agentEdits.finishedGen
	m, _ = m.loadAgentEditsFinishedCmd()
	next, _ := m.handleAgentEditsFinishedList(agentEditsFinishedListMsg{gen: older, sessions: []store.AgentEditSession{{Session: "stale"}}})
	if m = next.(Model); len(m.agentEdits.finished) != 0 {
		t.Fatal("an older load's answer does not overwrite the newer one")
	}
	next, _ = m.handleAgentEditsFinishedList(agentEditsFinishedListMsg{gen: m.agentEdits.finishedGen, sessions: []store.AgentEditSession{{Session: "fresh"}}})
	if m = next.(Model); len(m.agentEdits.finished) != 1 || m.agentEdits.finished[0].Session != "fresh" {
		t.Fatalf("the newest lands: %+v", m.agentEdits.finished)
	}
}

func TestAClosedSessionsRefusalCarriesThePhraseTheToolsActOn(t *testing.T) {
	if !strings.Contains(staging.ErrClosedForEdits.Error(), extmsg.PhraseStartNewSession) {
		t.Fatalf("the MCP tools open a new session on %q; staging's refusal says %q", extmsg.PhraseStartNewSession, staging.ErrClosedForEdits)
	}
	_, _, receiver, registry, _ := agentEditsModel(t, store.AgentEditPolicyReview)
	receiver.Handle("write --edit-session=e1\nmain.go\n---\nx\n")
	_, _, err := registry.MarkAgentEnded("e1", "")
	must(t, err)
	_, err = registry.BeginApply("e1")
	must(t, err)
	_, err = registry.Write("e1", "", "main.go", "y\n")
	if !errors.Is(err, staging.ErrClosedForEdits) {
		t.Fatalf("a refused edit wraps ErrClosedForEdits: %v", err)
	}
}

func TestAStaleReadRefusalIsWordedForTheCommandLine(t *testing.T) {
	text := agentEditErrorText(&staging.StaleReadError{Rel: "a.go", Change: "agent a's replace L1", Conflict: "a whole-file write would undo that"})
	if text != "a.go changed since you read it: agent a's replace L1 — a whole-file write would undo that; read it again (ntee --msg read) and stage your change on the current content" {
		t.Fatalf("stale: %q", text)
	}
	if text := agentEditErrorText(&staging.OldTextAmbiguousError{Rel: "a.go", Count: 2, Lines: "2, 4"}); !strings.Contains(text, "add #L<n> to the path") || !strings.Contains(text, "--msg-replace-all") {
		t.Fatalf("ambiguous: %q", text)
	}
}

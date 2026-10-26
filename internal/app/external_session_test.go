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

// sessionRecord is a session request as the receiver hands it over.
func sessionRecord(command, session, path, note string) store.ExternalRecord {
	record := externalRecord(command)
	if path != "" {
		record.Paths = []string{path}
	}
	record.Base = store.ExternalDefaultBase
	record.Session = session
	record.LineNote = note
	return record
}

// sendSession delivers a session request and returns the channel its sender
// waits on.
func sendSession(m Model, record store.ExternalRecord) (Model, chan string) {
	reply := make(chan string, 1)
	m, cmd := deliver(m, externalMessageMsg{record: record, reply: reply})
	return settleExternal(m, cmd), reply
}

// noReply asserts the sender is still waiting.
func noReply(t *testing.T, reply chan string) {
	t.Helper()
	select {
	case outcome := <-reply:
		t.Fatalf("the sender got %q while the user hasn't answered", outcome)
	default:
	}
}

func gotReply(t *testing.T, reply chan string) string {
	t.Helper()
	select {
	case outcome := <-reply:
		return outcome
	default:
		t.Fatal("the sender got no answer")
		return ""
	}
}

// startedSession is a session "walk1" the user accepted, on its first step.
func startedSession(t *testing.T) (Model, chan string) {
	t.Helper()
	m, _ := noteWorkspace(t)
	m, _, _ = externalModel(t, m, true)
	m, reply := sendSession(m, sessionRecord("open", "walk1", "long.go#L40", "# Step one\nwhy line 40"))
	if !m.choice.open() || !strings.Contains(m.choice.title, "Agent session · walk1") {
		t.Fatalf("a new session asks first, even from the query view: %+v", m.choice)
	}
	noReply(t, reply)
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Start session
	m = settleExternal(next.(Model), cmd)
	if m.session.id != "walk1" || m.session.step != 1 || !m.sessionStepVisible() || m.edit.cy != 39 {
		t.Fatalf("started: session=%+v visible=%v cy=%d", m.session, m.sessionStepVisible(), m.edit.cy)
	}
	noReply(t, reply)
	return m, reply
}

func TestSessionStartShowsTheStepBox(t *testing.T) {
	m, _ := startedSession(t)
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"walk1 · step 1 · L40", "Step one", "enter next · esc end session", "● session walk1 · step 1"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("view is missing %q:\n%s", want, plain)
		}
	}
}

func TestSessionEnterAsksForTheNextStepWhichJumpsWithoutAsking(t *testing.T) {
	m, reply := startedSession(t)
	m = key(m, keyPress(tea.KeyEnter))
	if outcome := gotReply(t, reply); outcome != "next" {
		t.Fatalf("Enter answers next: %q", outcome)
	}
	if m.sessionStepVisible() || m.session.id != "walk1" {
		t.Fatal("the box closes, the session stays on")
	}
	// A late wait still gets the answer.
	m, waited := sendSession(m, sessionRecord("wait", "walk1", "", ""))
	if outcome := gotReply(t, waited); outcome != "next" {
		t.Fatalf("wait after the answer = %q", outcome)
	}

	m.mode = modeEdit // the user is busy editing — a step still doesn't ask
	m, reply = sendSession(m, sessionRecord("open", "walk1", "main.go#L3", "step two"))
	if m.choice.open() || m.openRel != "main.go" || m.edit.cy != 2 || m.session.step != 2 || !m.sessionStepVisible() {
		t.Fatalf("step 2: choice=%v open=%q cy=%d step=%d", m.choice.open(), m.openRel, m.edit.cy, m.session.step)
	}
	noReply(t, reply)
}

func TestSessionEscEndsItAndLaterStepsAreRefused(t *testing.T) {
	m, reply := startedSession(t)
	m = key(m, keyPress(tea.KeyEscape))
	outcome := gotReply(t, reply)
	if !extmsg.IsEndedReply(outcome) || !strings.Contains(outcome, "closed by the user") {
		t.Fatalf("Esc ends: %q", outcome)
	}
	if m.session.id != "" || m.lineNoteVisible() || m.mode != modeEdit {
		t.Fatalf("after Esc: session=%+v visible=%v mode=%v", m.session, m.lineNoteVisible(), m.mode)
	}
	m, reply = sendSession(m, sessionRecord("open", "walk1", "main.go#L3", "again"))
	if outcome := gotReply(t, reply); !extmsg.IsEndedReply(outcome) || !strings.Contains(outcome, "stop sending steps") || m.choice.open() {
		t.Fatalf("an ended session is refused, not restarted: %q", outcome)
	}
}

func TestSessionDeclineEndsIt(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, _, _ = externalModel(t, m, true)
	m, reply := sendSession(m, sessionRecord("open", "walk2", "long.go#L40", "why"))
	m = key(m, keyPress(tea.KeyRight)) // Decline
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleExternal(next.(Model), cmd)
	if outcome := gotReply(t, reply); !strings.Contains(outcome, "declined by the user") || m.session.id != "" {
		t.Fatalf("decline: %q session=%+v", outcome, m.session)
	}
}

func TestSessionAgentEndAndOtherSessions(t *testing.T) {
	m, reply := startedSession(t)
	m, other := sendSession(m, sessionRecord("open", "walk9", "main.go#L1", "x"))
	if outcome := gotReply(t, other); !extmsg.IsErrorReply(outcome) || !strings.Contains(outcome, "another session (walk1)") {
		t.Fatalf("a second session is refused: %q", outcome)
	}
	m, ended := sendSession(m, sessionRecord("end", "walk1", "", ""))
	if outcome := gotReply(t, ended); outcome != "ended session walk1 finished" {
		t.Fatalf("end replies %q", outcome)
	}
	if outcome := gotReply(t, reply); !strings.Contains(outcome, "finished by the agent") {
		t.Fatalf("the waiting step hears it: %q", outcome)
	}
	if m.session.id != "" || m.lineNoteVisible() {
		t.Fatal("end closes the box")
	}
	m, waited := sendSession(m, sessionRecord("wait", "nope", "", ""))
	if outcome := gotReply(t, waited); !strings.Contains(outcome, "no session nope") {
		t.Fatalf("wait on an unknown session: %q", outcome)
	}
}

func TestSessionStepIsReadOnly(t *testing.T) {
	m, _ := startedSession(t)
	before := strings.Join(m.edit.lines, "\n")
	for _, msg := range []tea.KeyPressMsg{typeRune('x'), keyPress(tea.KeyTab), keyPress(tea.KeyBackspace), ctrlKey('s')} {
		m = key(m, msg)
	}
	next, _ := m.Update(tea.PasteMsg{Content: "pasted"})
	m = next.(Model)
	if strings.Join(m.edit.lines, "\n") != before || !strings.Contains(m.notice, "read-only") {
		t.Fatalf("the step is read-only: notice=%q", m.notice)
	}
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyPgDown))
	if m.edit.cy <= 39 || !m.sessionStepVisible() {
		t.Fatalf("moving still works: cy=%d", m.edit.cy)
	}
}

func TestSessionBoxComesBackAfterVisitingAnotherFile(t *testing.T) {
	m, _ := startedSession(t)
	m = m.openFileAt("main.go")
	m.mode = modeEdit
	if m.sessionStepVisible() {
		t.Fatal("the box belongs to long.go")
	}
	m = m.openFileAt("long.go")
	m.mode = modeEdit
	if !m.sessionStepVisible() {
		t.Fatal("back on long.go the step shows again")
	}
}

func TestSessionStepCrossesRepos(t *testing.T) {
	m, _ := workspaceModel(t, nil)
	m, _, _ = externalModel(t, m, true)
	m = enterRepo(t, m, "web")
	m, _ = sendSession(m, sessionRecord("open", "repo", "apps/web/main.go#L1", "one"))
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleExternal(next.(Model), cmd)
	m = key(m, keyPress(tea.KeyEnter)) // next
	m, _ = sendSession(m, sessionRecord("open", "repo", "libs/core/lib.go#L1", "two"))
	if m.choice.open() || m.activeRepo != "libs/core" || m.openRel != "lib.go" || !m.sessionStepVisible() {
		t.Fatalf("a step switches repo without asking: choice=%v repo=%q open=%q err=%q", m.choice.open(), m.activeRepo, m.openRel, m.errText)
	}
}

func TestSessionReplayIsAPlainNote(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, _, _ = externalModel(t, m, true)
	record := sessionRecord("open", "walk1", "long.go#L40", "replayed")
	next, cmd := m.handleExternalMessage(externalMessageMsg{record: record, replay: true})
	m = settleExternal(next.(Model), cmd)
	if m.session.id != "" || !m.lineNoteVisible() || m.lineNote.session != "" {
		t.Fatalf("replay: session=%+v note=%+v", m.session, m.lineNote)
	}
}

func TestReceiverWaitsForASessionAnswer(t *testing.T) {
	_, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644))
	settings := store.NewMemoryExternalSettings(store.ExternalSettings{Enabled: true, HistoryLimit: 10})
	history := store.NewMemoryExternalHistory()
	receiver := NewExternalReceiver(root, settings, history)
	receiver.sessionWait = 50 * time.Millisecond
	delivered := make(chan externalMessageMsg, 2)
	receiver.SetSink(func(msg any) { delivered <- msg.(externalMessageMsg) })

	replies := make(chan string, 1)
	go func() { replies <- receiver.Handle("open --session=w\na.go#L1\n---\nwhy\n") }()
	msg := <-delivered
	if msg.record.Session != "w" {
		t.Fatalf("record session = %q", msg.record.Session)
	}
	if reply := <-replies; !strings.HasPrefix(reply, "ok waiting") {
		t.Fatalf("an unanswered step says waiting: %q", reply)
	}

	go func() { replies <- receiver.Handle("wait --session=w\n") }()
	msg = <-delivered
	msg.reply <- "next"
	if reply := <-replies; reply != "ok next" {
		t.Fatalf("the answer reaches the sender: %q", reply)
	}
	if records, _ := history.LoadExternal(); len(records) != 1 || records[0].Session != "w" {
		t.Fatalf("only the step is recorded, with its session: %+v", records)
	}
}

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
		t.Fatalf("the sender got %q before the session ended", outcome)
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

// queueStep sends a step and checks it was queued at once.
func queueStep(t *testing.T, m Model, session, path, note string) Model {
	t.Helper()
	m, reply := sendSession(m, sessionRecord("open", session, path, note))
	if outcome := gotReply(t, reply); !strings.Contains(outcome, "queued") {
		t.Fatalf("a step returns at once as queued: %q", outcome)
	}
	return m
}

// startedSession is a session "walk1" with three queued steps — the agent
// sent them all without waiting — that the user accepted.
func startedSession(t *testing.T) Model {
	t.Helper()
	m, _ := noteWorkspace(t)
	m, _, _ = externalModel(t, m, true)
	m = queueStep(t, m, "walk1", "long.go#L40", "# Step one\nwhy line 40")
	if !m.choice.open() || !strings.Contains(m.choice.title, "Agent session · walk1") {
		t.Fatalf("a new session asks first, even from the query view: %+v", m.choice)
	}
	m = queueStep(t, m, "walk1", "main.go#L3", "step two")
	m = queueStep(t, m, "walk1", "long.go#L90", "step three")
	if m.mode != modeQuery || len(m.session.steps) != 3 {
		t.Fatalf("steps queue behind the confirm: mode=%v steps=%d", m.mode, len(m.session.steps))
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter)) // Start session
	m = settleExternal(next.(Model), cmd)
	if !m.session.started || m.session.current != 0 || !m.sessionStepVisible() || m.openRel != "long.go" || m.edit.cy != 39 {
		t.Fatalf("started: session=%+v visible=%v open=%q cy=%d", m.session, m.sessionStepVisible(), m.openRel, m.edit.cy)
	}
	return m
}

func TestSessionStartShowsTheStepBox(t *testing.T) {
	m := startedSession(t)
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"walk1 · step 1/3 · L40", "Step one", "enter next · esc end session", "● session walk1 · step 1/3"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("view is missing %q:\n%s", want, plain)
		}
	}
}

func TestSessionEnterAndBackspaceWalkTheQueue(t *testing.T) {
	m := startedSession(t)
	m = key(m, keyPress(tea.KeyEnter))
	if m.choice.open() || m.openRel != "main.go" || m.edit.cy != 2 || m.session.current != 1 || !m.sessionStepVisible() {
		t.Fatalf("Enter shows step 2 at once: open=%q cy=%d current=%d", m.openRel, m.edit.cy, m.session.current)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "⌫ back") {
		t.Fatalf("step 2 offers back:\n%s", plain)
	}
	m = key(m, keyPress(tea.KeyBackspace))
	if m.openRel != "long.go" || m.edit.cy != 39 || m.session.current != 0 {
		t.Fatalf("Backspace goes back: open=%q cy=%d current=%d", m.openRel, m.edit.cy, m.session.current)
	}
	m = key(m, keyPress(tea.KeyBackspace))
	if m.session.current != 0 || m.notice != "this is the first step" {
		t.Fatalf("no step before the first: %q", m.notice)
	}
}

func TestSessionPastTheLastStepWaitsForTheAgent(t *testing.T) {
	m := startedSession(t)
	for range 3 {
		m = key(m, keyPress(tea.KeyEnter))
	}
	if m.sessionStepVisible() || m.session.current != 3 || m.session.id != "walk1" || !strings.Contains(m.notice, "waiting for the agent") {
		t.Fatalf("past the last step: visible=%v current=%d notice=%q", m.sessionStepVisible(), m.session.current, m.notice)
	}
	m = queueStep(t, m, "walk1", "main.go#L1", "step four")
	if !m.sessionStepVisible() || m.openRel != "main.go" || m.session.current != 3 {
		t.Fatalf("a step the user waits for shows at once: open=%q current=%d", m.openRel, m.session.current)
	}
}

func TestSessionEndFinishesAfterTheLastStep(t *testing.T) {
	m := startedSession(t)
	m, waited := sendSession(m, sessionRecord("wait", "walk1", "", ""))
	noReply(t, waited)
	m, ended := sendSession(m, sessionRecord("end", "walk1", "", ""))
	if outcome := gotReply(t, ended); !strings.Contains(outcome, "ends after step 3") {
		t.Fatalf("end marks the session complete: %q", outcome)
	}
	m = key(m, keyPress(tea.KeyEnter))
	m = key(m, keyPress(tea.KeyEnter))
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "enter finish") {
		t.Fatalf("the last step of a complete session finishes:\n%s", plain)
	}
	noReply(t, waited)
	m = key(m, keyPress(tea.KeyEnter))
	if m.session.id != "" || m.lineNoteVisible() {
		t.Fatal("Enter on the last step finishes the session")
	}
	if outcome := gotReply(t, waited); !strings.HasPrefix(outcome, "finished") || !strings.Contains(outcome, "all 3 steps") {
		t.Fatalf("wait hears the user finished: %q", outcome)
	}
	_, reply := sendSession(m, sessionRecord("open", "walk1", "main.go#L3", "more"))
	if outcome := gotReply(t, reply); !extmsg.IsEndedReply(outcome) {
		t.Fatalf("a finished session takes no more steps: %q", outcome)
	}
}

func TestSessionEndWhileTheUserWaitsFinishesIt(t *testing.T) {
	m := startedSession(t)
	for range 3 {
		m = key(m, keyPress(tea.KeyEnter))
	}
	m, _ = sendSession(m, sessionRecord("end", "walk1", "", ""))
	if m.session.id != "" || !strings.Contains(m.notice, "finished") {
		t.Fatalf("end with the user past the last step finishes: %+v %q", m.session, m.notice)
	}
}

func TestSessionEscEndsItAndLaterStepsAreRefused(t *testing.T) {
	m := startedSession(t)
	m, waited := sendSession(m, sessionRecord("wait", "walk1", "", ""))
	m = key(m, keyPress(tea.KeyEscape))
	if outcome := gotReply(t, waited); !extmsg.IsEndedReply(outcome) || !strings.Contains(outcome, "closed by the user") {
		t.Fatalf("Esc ends: %q", outcome)
	}
	if m.session.id != "" || m.lineNoteVisible() || m.mode != modeEdit {
		t.Fatalf("after Esc: session=%+v visible=%v mode=%v", m.session, m.lineNoteVisible(), m.mode)
	}
	m, reply := sendSession(m, sessionRecord("open", "walk1", "main.go#L3", "again"))
	if outcome := gotReply(t, reply); !extmsg.IsEndedReply(outcome) || !strings.Contains(outcome, "stop sending steps") || m.choice.open() {
		t.Fatalf("an ended session is refused, not restarted: %q", outcome)
	}
}

func TestSessionDeclineEndsIt(t *testing.T) {
	m, _ := noteWorkspace(t)
	m, _, _ = externalModel(t, m, true)
	m = queueStep(t, m, "walk2", "long.go#L40", "why")
	m, waited := sendSession(m, sessionRecord("wait", "walk2", "", ""))
	m = key(m, keyPress(tea.KeyRight)) // Decline
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleExternal(next.(Model), cmd)
	if outcome := gotReply(t, waited); !strings.Contains(outcome, "declined by the user") || m.session.id != "" {
		t.Fatalf("decline: %q session=%+v", outcome, m.session)
	}
}

func TestSessionOtherSessionsAndUnknownIDs(t *testing.T) {
	m := startedSession(t)
	m, other := sendSession(m, sessionRecord("open", "walk9", "main.go#L1", "x"))
	if outcome := gotReply(t, other); !extmsg.IsErrorReply(outcome) || !strings.Contains(outcome, "another session (walk1)") {
		t.Fatalf("a second session is refused: %q", outcome)
	}
	_, waited := sendSession(m, sessionRecord("wait", "nope", "", ""))
	if outcome := gotReply(t, waited); !strings.Contains(outcome, "no session nope") {
		t.Fatalf("wait on an unknown session: %q", outcome)
	}
}

func TestSessionStepIsReadOnly(t *testing.T) {
	m := startedSession(t)
	before := strings.Join(m.edit.lines, "\n")
	for _, msg := range []tea.KeyPressMsg{typeRune('x'), keyPress(tea.KeyTab), ctrlKey('s')} {
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
	m := startedSession(t)
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
	m = queueStep(t, m, "repo", "apps/web/main.go#L1", "one")
	m = queueStep(t, m, "repo", "libs/core/lib.go#L1", "two")
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleExternal(next.(Model), cmd)
	m = key(m, keyPress(tea.KeyEnter)) // next
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

func TestReceiverWaitsForASessionToEnd(t *testing.T) {
	_, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644))
	settings := store.NewMemoryExternalSettings(store.ExternalSettings{Enabled: true, HistoryLimit: 10})
	history := store.NewMemoryExternalHistory()
	receiver := NewExternalReceiver(root, settings, history)
	receiver.sessionWait = 50 * time.Millisecond
	delivered := make(chan externalMessageMsg, 2)
	receiver.SetSink(func(msg any) { delivered <- msg.(externalMessageMsg) })

	replies := make(chan string, 1)
	go func() { replies <- receiver.Handle("wait --session=w\n") }()
	<-delivered
	if reply := <-replies; !strings.HasPrefix(reply, "ok waiting") {
		t.Fatalf("an unanswered wait says waiting: %q", reply)
	}

	go func() { replies <- receiver.Handle("open --session=w\na.go#L1\n---\nwhy\n") }()
	msg := <-delivered
	msg.reply <- "step 1 queued"
	if reply := <-replies; reply != "ok step 1 queued" || msg.record.Session != "w" {
		t.Fatalf("the step's outcome reaches the sender: %q (session %q)", reply, msg.record.Session)
	}
	if records, _ := history.LoadExternal(); len(records) != 1 || records[0].Session != "w" {
		t.Fatalf("only the step is recorded, with its session: %+v", records)
	}
}

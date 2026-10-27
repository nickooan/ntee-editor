package app

import (
	"os"
	"os/exec"
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
	if msg.diffSession {
		t.Fatal("--session= is a note session")
	}

	go func() { replies <- receiver.Handle("wait --diff-session=d\n") }()
	if msg := <-delivered; !msg.diffSession || msg.record.Session != "d" {
		t.Fatalf("--diff-session= reaches the editor as a diff session: %+v", msg)
	}
	if reply := <-replies; !strings.Contains(reply, "--msg-diff-session d wait") {
		t.Fatalf("a diff session's wait names its flag: %q", reply)
	}
}

// sendDiffSession delivers a diff session request (--diff-session=) and
// returns the channel its sender waits on.
func sendDiffSession(m Model, record store.ExternalRecord) (Model, chan string) {
	reply := make(chan string, 1)
	m, cmd := deliver(m, externalMessageMsg{record: record, diffSession: true, reply: reply})
	return settleExternal(m, cmd), reply
}

// keySettled presses a key and runs what it starts (a step's diff).
func keySettled(m Model, msg tea.KeyPressMsg) Model {
	next, cmd := m.Update(msg)
	return settleExternal(next.(Model), cmd)
}

// diffSessionWorkspace is gitReviewWorkspace plus apps/web/same.go, committed
// and unchanged, with external messages on and the nested repos scanned.
func diffSessionWorkspace(t *testing.T) Model {
	t.Helper()
	m, root := gitReviewWorkspace(t)
	web := filepath.Join(root, "apps", "web")
	must(t, os.WriteFile(filepath.Join(web, "same.go"), []byte("package main\n\nvar same = 1\n"), 0o644))
	for _, args := range [][]string{{"add", "same.go"}, {"commit", "-q", "-m", "same"}} {
		cmd := exec.Command("git", append([]string{"-C", web, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	m, _ = deliver(m, workspaceReposMsg{repos: []string{"apps/web", "libs/core"}})
	m, _, _ = externalModel(t, m, true)
	return m
}

// queueDiffStep sends a diff session step and checks it was queued.
func queueDiffStep(t *testing.T, m Model, session, path, note string) Model {
	t.Helper()
	m, reply := sendDiffSession(m, sessionRecord("open", session, path, note))
	if outcome := gotReply(t, reply); !strings.Contains(outcome, "queued") {
		t.Fatalf("a diff step returns at once as queued: %q", outcome)
	}
	return m
}

// startedDiffSession is diff session "walk1" with two steps the user
// accepted: a changed file, then an unchanged one.
func startedDiffSession(t *testing.T) Model {
	t.Helper()
	m := diffSessionWorkspace(t)
	m = queueDiffStep(t, m, "walk1", "apps/web/main.go#L3", "# Step one\nwhy run")
	if !m.choice.open() || !strings.Contains(m.choice.title, "Agent diff session · walk1") {
		t.Fatalf("a diff session asks first: %+v", m.choice)
	}
	m = queueDiffStep(t, m, "walk1", "apps/web/same.go#L3", "step two")
	m = keySettled(m, keyPress(tea.KeyEnter)) // Start session
	if !m.session.started || !m.session.diff || m.mode != modeDiff || m.openRel != "apps/web/main.go" || !m.sessionStepVisible() {
		t.Fatalf("started: mode=%v open=%q visible=%v err=%q session=%+v", m.mode, m.openRel, m.sessionStepVisible(), m.errText, m.session)
	}
	return m
}

func TestDiffSessionShowsTheStepBoxInTheDiff(t *testing.T) {
	m := startedDiffSession(t)
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"@diff", "walk1 · step 1/2 · L3", "Step one", "● diff session walk1 · step 1/2", "Esc end session"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("view is missing %q:\n%s", want, plain)
		}
	}
}

func TestDiffSessionKeysWalkTheStepsInTheDiff(t *testing.T) {
	m := startedDiffSession(t)
	m = keySettled(m, keyPress(tea.KeyEnter))
	if m.mode != modeDiff || m.openRel != "apps/web/same.go" || m.session.current != 1 || !m.sessionStepVisible() {
		t.Fatalf("Enter shows step 2 in its diff, even unchanged: mode=%v open=%q current=%d err=%q", m.mode, m.openRel, m.session.current, m.errText)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "step two") || !strings.Contains(plain, "+0 -0") {
		t.Fatalf("the unchanged file's diff shows with its note:\n%s", plain)
	}
	m = keySettled(m, keyPress(tea.KeyBackspace))
	if m.mode != modeDiff || m.openRel != "apps/web/main.go" || m.session.current != 0 || !m.sessionStepVisible() {
		t.Fatalf("Backspace goes back to step 1's diff: mode=%v open=%q current=%d", m.mode, m.openRel, m.session.current)
	}
	m = key(m, keyPress(tea.KeyEscape))
	if m.session.id != "" || m.mode != modeDiff || m.lineNoteVisibleInDiff() {
		t.Fatalf("Esc ends the session and keeps the diff: session=%q mode=%v", m.session.id, m.mode)
	}
	m = key(m, keyPress(tea.KeyEscape))
	if m.mode != modeEdit || m.lineNoteVisible() {
		t.Fatalf("a second Esc leaves the diff, no note left behind: mode=%v", m.mode)
	}
}

func TestDiffSessionStepIsReadOnly(t *testing.T) {
	m := startedDiffSession(t)
	m = key(m, typeRune('x'))
	if m.mode != modeDiff || !m.sessionStepVisible() || !strings.Contains(m.notice, "read-only") {
		t.Fatalf("typing is swallowed: mode=%v notice=%q", m.mode, m.notice)
	}
	row := m.diffPosition.row
	m = key(m, keyPress(tea.KeyUp))
	if m.diffPosition.row != row-1 || !m.sessionStepVisible() {
		t.Fatalf("moving still works: row %d → %d", row, m.diffPosition.row)
	}
}

func TestDiffSessionTallNoteScrollsWithShiftArrows(t *testing.T) {
	m := diffSessionWorkspace(t)
	m = queueDiffStep(t, m, "walk1", "apps/web/main.go#L1", strings.Repeat("a line of the note\n", 60))
	m = keySettled(m, keyPress(tea.KeyEnter))
	row := m.diffPosition.row
	m = key(m, shiftKey(tea.KeyDown))
	if m.lineNote.scroll != 1 || m.diffPosition.row != row {
		t.Fatalf("Shift+↓ scrolls the tall note, not the diff: scroll=%d row %d → %d", m.lineNote.scroll, row, m.diffPosition.row)
	}
}

func TestDiffSessionFallsBackToTheFileOnABadRevision(t *testing.T) {
	m := diffSessionWorkspace(t)
	record := sessionRecord("open", "walk1", "apps/web/main.go#L3", "bad base")
	record.Args, record.Base = []string{"no-such-rev"}, "no-such-rev"
	m, reply := sendDiffSession(m, record)
	gotReply(t, reply)
	m = keySettled(m, keyPress(tea.KeyEnter))
	if m.mode != modeEdit || !m.sessionStepVisible() || !strings.HasPrefix(m.errText, "git diff:") {
		t.Fatalf("the step's note shows on the file, the reason in the status bar: mode=%v err=%q", m.mode, m.errText)
	}
}

func TestSessionKindsAndIDsDoNotMix(t *testing.T) {
	m := diffSessionWorkspace(t)
	m = queueDiffStep(t, m, "walk1", "apps/web/main.go#L3", "one")
	m, reply := sendSession(m, sessionRecord("open", "walk1", "apps/web/main.go#L1", "x"))
	if outcome := gotReply(t, reply); !extmsg.IsErrorReply(outcome) || !strings.Contains(outcome, "is a diff session — send its steps with --msg-diff-session") {
		t.Fatalf("a note step for a diff session is refused: %q", outcome)
	}
	m, reply = sendDiffSession(m, sessionRecord("open", "walk9", "apps/web/main.go#L1", "x"))
	if outcome := gotReply(t, reply); !extmsg.IsErrorReply(outcome) || !strings.Contains(outcome, "another diff session (walk1)") {
		t.Fatalf("another id is refused while one waits to start: %q", outcome)
	}
	m = keySettled(m, keyPress(tea.KeyEnter))
	m, reply = sendSession(m, sessionRecord("open", "walk9", "apps/web/main.go#L1", "x"))
	if outcome := gotReply(t, reply); !extmsg.IsErrorReply(outcome) || !strings.Contains(outcome, "--msg-diff-session walk1 end") {
		t.Fatalf("another id is refused while one runs: %q", outcome)
	}
	if len(m.session.steps) != 1 || m.session.id != "walk1" {
		t.Fatalf("refused steps never queue: %+v", m.session)
	}

	note := startedSession(t)
	_, reply = sendDiffSession(note, sessionRecord("open", "walk1", "long.go#L1", "x"))
	if outcome := gotReply(t, reply); !extmsg.IsErrorReply(outcome) || !strings.Contains(outcome, "is a session — send its steps with --msg-session") {
		t.Fatalf("a diff step for a note session is refused: %q", outcome)
	}
}

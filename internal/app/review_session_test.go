package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/extmsg"
	"github.com/nickooan/ntee-editor/internal/store"
)

// sendReviewSession delivers a review session request (--review-session=)
// and returns the channel its sender waits on.
func sendReviewSession(m Model, record store.ExternalRecord) (Model, chan string) {
	reply := make(chan string, 1)
	m, cmd := deliver(m, externalMessageMsg{record: record, sessionKind: extmsg.SessionReview, reply: reply})
	return settleExternal(m, cmd), reply
}

// sendReviewFile sends one file of review session id and returns the reply.
func sendReviewFile(t *testing.T, m Model, id, path, note string) (Model, string) {
	t.Helper()
	m, reply := sendReviewSession(m, sessionRecord("review", id, path, note))
	return m, gotReply(t, reply)
}

// reviewSessionWorkspace is gitReviewWorkspace (apps/web/main.go modified,
// apps/web/new.go added, libs/core/lib.go deleted) with external messages on.
func reviewSessionWorkspace(t *testing.T) (Model, string, *store.MemoryExternalHistory) {
	t.Helper()
	m, root := gitReviewWorkspace(t)
	m, _, history := externalModel(t, m, true)
	return m, root, history
}

// startedReviewSession is review session "r1" with two files the agent sent
// before the user started it: main.go with a note on line 3, new.go bare.
func startedReviewSession(t *testing.T) (Model, string, *store.MemoryExternalHistory) {
	t.Helper()
	m, root, history := reviewSessionWorkspace(t)
	m, outcome := sendReviewFile(t, m, "r1", "apps/web/main.go#L3", "# Why\nmain now runs")
	if !strings.Contains(outcome, "file 1 queued") || !m.choice.open() || m.choice.title != "Agent review session · r1" {
		t.Fatalf("the first file asks to start: reply=%q choice=%+v", outcome, m.choice)
	}
	m, outcome = sendReviewFile(t, m, "r1", "apps/web/new.go", "")
	if outcome != "file 2 queued" {
		t.Fatalf("a file before the start queues: %q", outcome)
	}
	m = keySettled(m, keyPress(tea.KeyEnter)) // Start session
	if m.mode != modeReview || m.review.session != "r1" || !m.reviewSessionRunning() || len(m.review.files) != 2 {
		t.Fatalf("started: mode=%v review session=%q files=%d", m.mode, m.review.session, len(m.review.files))
	}
	for _, file := range m.review.files {
		if !file.loaded() {
			t.Fatalf("%s is still loading", file.workspaceRel)
		}
	}
	if records, _ := history.LoadExternal(); len(records) != 0 {
		t.Fatalf("nothing is saved while the session runs: %+v", records)
	}
	return m, root, history
}

func selectedReviewPath(m Model) string {
	file, _ := m.selectedReviewFile()
	return file.workspaceRel
}

func TestReviewSessionGrowsAndFollowsArrivals(t *testing.T) {
	m, _, _ := startedReviewSession(t)
	if selectedReviewPath(m) != "apps/web/new.go" || !m.review.follow {
		t.Fatalf("the view follows to the newest file: selected=%q follow=%v", selectedReviewPath(m), m.review.follow)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "● review session r1 · 2 files · following") || !strings.Contains(plain, "Esc quit session") {
		t.Fatalf("status shows the live session:\n%s", plain)
	}
	m, outcome := sendReviewFile(t, m, "r1", "libs/core/lib.go", "gone on purpose")
	if outcome != "file 3 added to the review" || selectedReviewPath(m) != "libs/core/lib.go" || len(m.review.files) != 3 {
		t.Fatalf("an arrival joins and is followed: reply=%q selected=%q files=%d", outcome, selectedReviewPath(m), len(m.review.files))
	}
	if paths := []string{m.review.files[0].workspaceRel, m.review.files[1].workspaceRel, m.review.files[2].workspaceRel}; paths[0] != "apps/web/main.go" || paths[2] != "libs/core/lib.go" {
		t.Fatalf("files stay in tree order: %v", paths)
	}
}

func TestReviewSessionStopsFollowingWhenTheUserMoves(t *testing.T) {
	m, _, _ := startedReviewSession(t)
	m = keySettled(m, shiftKey(tea.KeyUp))
	if selectedReviewPath(m) != "apps/web/main.go" || m.review.follow {
		t.Fatalf("moving off the newest file stops following: selected=%q follow=%v", selectedReviewPath(m), m.review.follow)
	}
	m, _ = sendReviewFile(t, m, "r1", "libs/core/lib.go", "")
	if selectedReviewPath(m) != "apps/web/main.go" || !m.review.unseen["libs/core/lib.go"] || !strings.Contains(m.notice, "+ file from the agent: libs/core/lib.go · 1 new") {
		t.Fatalf("an arrival is marked, not followed: selected=%q unseen=%v notice=%q", selectedReviewPath(m), m.review.unseen, m.notice)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "· 1 new") || !strings.Contains(plain, "•") {
		t.Fatalf("the new file is marked:\n%s", plain)
	}
	if index := m.review.fileByPath["libs/core/lib.go"]; !m.review.files[index].loaded() {
		t.Fatal("an unseen file still loads in the background")
	}
	m = keySettled(m, shiftKey(tea.KeyDown))
	m = keySettled(m, shiftKey(tea.KeyDown))
	if selectedReviewPath(m) != "libs/core/lib.go" || !m.review.follow || len(m.review.unseen) != 0 {
		t.Fatalf("back on the newest file the view follows again: selected=%q follow=%v unseen=%v", selectedReviewPath(m), m.review.follow, m.review.unseen)
	}
}

func TestReviewSessionNoteBoxSitsOnItsLine(t *testing.T) {
	m, _, _ := startedReviewSession(t)
	m = keySettled(m, shiftKey(tea.KeyUp)) // main.go, its note on line 3
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	anchor := -1
	for i, line := range lines {
		if strings.Contains(line, "func main() { run() }") {
			anchor = i
		}
	}
	if anchor < 0 || !strings.Contains(lines[anchor+1], "note · L3") || !strings.Contains(strings.Join(lines, "\n"), "main now runs") {
		t.Fatalf("the box sits under line 3:\n%s", strings.Join(lines, "\n"))
	}

	m = keySettled(m, shiftKey(tea.KeyDown)) // new.go, no note
	if plain := ansi.Strip(m.View().Content); strings.Contains(plain, "note · L") || strings.Contains(plain, "main now runs") {
		t.Fatalf("a file without a note shows no box:\n%s", plain)
	}
	m, _ = sendReviewFile(t, m, "r1", "apps/web/new.go", "no line, so the first change")
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "─ note ─") || !strings.Contains(plain, "no line, so the first change") {
		t.Fatalf("an unanchored note sits at the first change:\n%s", plain)
	}
}

func TestReviewSessionResendRefreshesTheFile(t *testing.T) {
	m, root, _ := startedReviewSession(t)
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "main.go"), []byte("package main\n\nfunc main() { run(); stop() }\n\nfunc stop() {}\n"), 0o644))
	m, outcome := sendReviewFile(t, m, "r1", "apps/web/main.go#L5", "stop added")
	file := m.review.files[m.review.fileByPath["apps/web/main.go"]]
	if outcome != "file apps/web/main.go refreshed" || !file.loaded() || file.document.adds != 3 {
		t.Fatalf("a resend reloads the diff: reply=%q adds=%d", outcome, file.document.adds)
	}
	// A note on another line is added, not swapped in: the file now has
	// both, the new one active, the first written against the old content.
	if len(file.notes) != 2 || file.notes[0].line != 3 || file.notes[1].line != 5 || file.notes[1].overlay.text != "stop added" || file.activeNote != 1 {
		t.Fatalf("a note on a new line is added and active: %+v active=%d", file.notes, file.activeNote)
	}
	if !file.noteIsEarlier(file.notes[0]) || file.noteIsEarlier(file.notes[1]) {
		t.Fatal("the note sent before the change is earlier, the new one is not")
	}
	if len(m.session.reviews) != 3 || reviewFileCount(m.session.reviews) != 2 {
		t.Fatalf("the queue keeps one entry per note: %+v", m.session.reviews)
	}

	// The same line again replaces that note; no note keeps them all.
	m, _ = sendReviewFile(t, m, "r1", "apps/web/main.go#L5", "stop added, now tested")
	m, outcome = sendReviewFile(t, m, "r1", "apps/web/main.go", "")
	file = m.review.files[m.review.fileByPath["apps/web/main.go"]]
	if outcome != "file apps/web/main.go refreshed" || len(file.notes) != 2 || file.notes[1].overlay.text != "stop added, now tested" {
		t.Fatalf("same line replaces, no note keeps: reply=%q notes=%+v", outcome, file.notes)
	}
	if len(m.session.reviews) != 3 {
		t.Fatalf("neither resend adds a queue entry: %+v", m.session.reviews)
	}
}

func TestReviewSessionStepsThroughAFilesNotes(t *testing.T) {
	m, root, _ := startedReviewSession(t)
	must(t, os.WriteFile(filepath.Join(root, "apps", "web", "main.go"), []byte("package main\n\nfunc main() { run(); stop() }\n\nfunc stop() {}\n"), 0o644))
	m, _ = sendReviewFile(t, m, "r1", "apps/web/main.go#L5", "stop added")
	if selectedReviewPath(m) != "apps/web/main.go" {
		t.Fatalf("the resent file is followed: %q", selectedReviewPath(m))
	}
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "note 2/2 · L5") || !strings.Contains(plain, "stop added") || !strings.Contains(plain, "[/] note") {
		t.Fatalf("the active note's box counts the notes and the hint names the keys:\n%s", plain)
	}
	if !strings.Contains(plain, "◆2") {
		t.Fatalf("the left pane counts the file's notes:\n%s", plain)
	}
	file, _ := m.selectedReviewFile()
	markers := file.noteMarkerRows()
	if len(markers) != 1 || !markers[file.noteRow(file.notes[0])] {
		t.Fatalf("only the note not on screen is marked: %v", markers)
	}

	m = key(m, keyPress(']')) // wraps to the first note
	file, _ = m.selectedReviewFile()
	if file.activeNote != 0 || m.review.position.row != file.noteRow(file.notes[0]) {
		t.Fatalf("] steps to the next note and its line: active=%d row=%d", file.activeNote, m.review.position.row)
	}
	plain = ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "note 1/2 · L3 · earlier") || !strings.Contains(plain, "main now runs") {
		t.Fatalf("the first note shows, marked earlier:\n%s", plain)
	}
	// The box may cover the line's text, never the gutter.
	if !strings.Contains(plain, " 5◆│") || strings.Contains(plain, " 3◆│") {
		t.Fatalf("the other note's line carries the gutter mark:\n%s", plain)
	}
	m = key(m, keyPress('['))
	if file, _ = m.selectedReviewFile(); file.activeNote != 1 {
		t.Fatalf("[ steps back: active=%d", file.activeNote)
	}
}

func TestQueuedReviewKeepsOneEntryPerNoteLine(t *testing.T) {
	bare := reviewRequest{workspaceRel: "a.go"}
	at3 := reviewRequest{workspaceRel: "a.go", line: 3, note: "three"}
	at3again := reviewRequest{workspaceRel: "a.go", line: 3, note: "three, again"}
	at9 := reviewRequest{workspaceRel: "a.go", line: 9, note: "nine"}
	other := reviewRequest{workspaceRel: "b.go"}

	queue := withQueuedReview(nil, bare)
	queue = withQueuedReview(queue, other)
	queue = withQueuedReview(queue, at3)
	if len(queue) != 2 || queue[0] != at3 {
		t.Fatalf("a first note takes the bare entry's place: %+v", queue)
	}
	queue = withQueuedReview(queue, at9)
	queue = withQueuedReview(queue, at3again)
	queue = withQueuedReview(queue, bare)
	if len(queue) != 3 || queue[0] != at3again || queue[2] != at9 || reviewFileCount(queue) != 2 {
		t.Fatalf("one entry per note line, a bare resend adds none: %+v", queue)
	}
}

func TestReviewSessionQuitAsksThenTellsTheAgent(t *testing.T) {
	m, _, history := startedReviewSession(t)
	m, waiter := sendReviewSession(m, sessionRecord("wait", "r1", "", ""))
	noReply(t, waiter)

	m = key(m, keyPress(tea.KeyEscape))
	if m.choice.kind != choiceQuitReviewSession || m.mode != modeReview {
		t.Fatalf("Esc asks first: choice=%+v mode=%v", m.choice, m.mode)
	}
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "Quit the review session?") || !strings.Contains(plain, "told to stop") {
		t.Fatalf("the confirm says what quitting does:\n%s", plain)
	}
	m = key(m, keyPress(tea.KeyEscape)) // Stay
	if m.choice.open() || m.mode != modeReview || !m.reviewSessionRunning() {
		t.Fatal("Esc on the confirm stays in the review")
	}
	noReply(t, waiter)

	m = key(m, keyPress(tea.KeyEscape))
	m = keySettled(m, keyPress(tea.KeyEnter)) // Quit
	if m.mode == modeReview || m.session.id != "" {
		t.Fatalf("Quit leaves the review: mode=%v session=%q", m.mode, m.session.id)
	}
	if outcome := gotReply(t, waiter); !extmsg.IsEndedReply(outcome) || !strings.Contains(outcome, "the user quit the review") {
		t.Fatalf("the agent hears the user quit: %q", outcome)
	}
	records, _ := history.LoadExternal()
	if len(records) != 1 {
		t.Fatalf("the session is saved once: %+v", records)
	}
	record := records[0]
	if record.Command != "review" || record.Session != "r1" || record.Base != store.ExternalDefaultBase || record.Status != store.ExternalReviewed ||
		strings.Join(record.Paths, ",") != "apps/web/main.go#L3,apps/web/new.go" || record.PathNotes[0] != "# Why\nmain now runs" || record.PathNotes[1] != "" {
		t.Fatalf("one record of the whole session: %+v", record)
	}
	if _, outcome := sendReviewFile(t, m, "r1", "libs/core/lib.go", ""); !extmsg.IsEndedReply(outcome) {
		t.Fatalf("a later file hears the session ended: %q", outcome)
	}
}

func TestReviewSessionEndSavesThenQuitFinishes(t *testing.T) {
	m, _, history := startedReviewSession(t)
	m, reply := sendReviewSession(m, sessionRecord("end", "r1", "", ""))
	if outcome := gotReply(t, reply); outcome != "session r1 complete — the user is reviewing 2 files" {
		t.Fatalf("end reply: %q", outcome)
	}
	if records, _ := history.LoadExternal(); len(records) != 1 || records[0].Note != "the agent ended the session" {
		t.Fatalf("end saves the session: %+v", records)
	}
	if !m.reviewSessionRunning() || !strings.Contains(ansi.Strip(m.View().Content), "· complete") {
		t.Fatal("the review stays open for the user, marked complete")
	}
	if _, outcome := sendReviewFile(t, m, "r1", "libs/core/lib.go", ""); !extmsg.IsErrorReply(outcome) || !strings.Contains(outcome, "already got end") {
		t.Fatalf("no files after end: %q", outcome)
	}
	m, waiter := sendReviewSession(m, sessionRecord("wait", "r1", "", ""))
	m = key(m, keyPress(tea.KeyEscape))
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "the agent has finished sending files") {
		t.Fatalf("the confirm knows the agent is done:\n%s", plain)
	}
	keySettled(m, keyPress(tea.KeyEnter))
	if outcome := gotReply(t, waiter); !strings.HasPrefix(outcome, "finished") {
		t.Fatalf("quitting a complete review finishes it: %q", outcome)
	}
	if records, _ := history.LoadExternal(); len(records) != 1 {
		t.Fatalf("still saved once: %+v", records)
	}
}

func TestReviewSessionDeclineIsSavedAsIgnored(t *testing.T) {
	m, _, history := reviewSessionWorkspace(t)
	m, _ = sendReviewFile(t, m, "r1", "apps/web/main.go", "")
	m = keySettled(m, typeRune('n')) // Decline
	if m.session.id != "" || m.mode == modeReview {
		t.Fatalf("declined: session=%q mode=%v", m.session.id, m.mode)
	}
	if records, _ := history.LoadExternal(); len(records) != 1 || records[0].Status != store.ExternalIgnored {
		t.Fatalf("a declined session is saved as ignored: %+v", records)
	}
	if _, outcome := sendReviewFile(t, m, "r1", "apps/web/new.go", ""); !extmsg.IsEndedReply(outcome) {
		t.Fatalf("a later file hears it was declined: %q", outcome)
	}
}

func TestReviewSessionIsTheOnlySession(t *testing.T) {
	m, _, history := startedReviewSession(t)
	plain := externalRecord("review", "apps/web/new.go")
	plain.Base = store.ExternalDefaultBase
	m, _, outcome := receive(m, history, plain)
	if !extmsg.IsErrorReply(outcome) || !strings.Contains(outcome, "a review session (r1) is running") || m.mode != modeReview {
		t.Fatalf("a plain request can't pull the user out of the review: %q mode=%v", outcome, m.mode)
	}
	m, reply := sendSession(m, sessionRecord("open", "walk9", "apps/web/main.go#L1", "x"))
	if outcome := gotReply(t, reply); !strings.Contains(outcome, "another review session (r1) is running") || !strings.Contains(outcome, "--msg-review-session r1 end") {
		t.Fatalf("another session is refused: %q", outcome)
	}
	m, reply = sendSession(m, sessionRecord("open", "r1", "apps/web/main.go#L1", "x"))
	if outcome := gotReply(t, reply); !strings.Contains(outcome, "is a review session — send its files with --msg-review-session") {
		t.Fatalf("the same id as another kind is refused: %q", outcome)
	}
	rebased := sessionRecord("review", "r1", "apps/web/main.go", "")
	rebased.Args, rebased.Base = []string{"main"}, "main"
	_, reply = sendReviewSession(m, rebased)
	if outcome := gotReply(t, reply); !strings.Contains(outcome, "compares against HEAD") {
		t.Fatalf("a later file can't change the base: %q", outcome)
	}
}

func TestReviewSessionReplayShowsTheNotes(t *testing.T) {
	m, _, history := startedReviewSession(t)
	m = key(m, keyPress(tea.KeyEscape))
	m = keySettled(m, keyPress(tea.KeyEnter)) // Quit
	records, _ := history.LoadExternal()
	next, cmd := m.handleExternalMessage(externalMessageMsg{record: records[0], replay: true})
	m = settleExternal(next.(Model), cmd)
	if m.mode != modeReview || m.review.session != "" || len(m.review.files) != 2 {
		t.Fatalf("a replay is a plain review: mode=%v session=%q files=%d", m.mode, m.review.session, len(m.review.files))
	}
	file := m.review.files[m.review.fileByPath["apps/web/main.go"]]
	if len(file.notes) != 1 || file.notes[0].overlay.text != "# Why\nmain now runs" || file.notes[0].line != 3 {
		t.Fatalf("the replay keeps each note on its line: %+v", file.notes)
	}
}

func TestReviewSessionReplayBringsBackEveryNote(t *testing.T) {
	m, _, history := startedReviewSession(t)
	m, _ = sendReviewFile(t, m, "r1", "apps/web/main.go#L1", "the package line")
	m = key(m, keyPress(tea.KeyEscape))
	m = keySettled(m, keyPress(tea.KeyEnter)) // Quit
	records, _ := history.LoadExternal()
	if got := strings.Join(records[0].Paths, " "); got != "apps/web/main.go#L3 apps/web/new.go apps/web/main.go#L1" || records[0].Raw != "review session r1 · 2 files" {
		t.Fatalf("the record repeats a file once per note: %q raw=%q", got, records[0].Raw)
	}
	m.externalHistory.records = records
	if plain := ansi.Strip(strings.Join(m.externalDetailLines(80), "\n")); !strings.Contains(plain, "review · 2 files") {
		t.Fatalf("the detail counts files, not notes:\n%s", plain)
	}

	next, cmd := m.handleExternalMessage(externalMessageMsg{record: records[0], replay: true})
	m = settleExternal(next.(Model), cmd)
	file := m.review.files[m.review.fileByPath["apps/web/main.go"]]
	if len(m.review.files) != 2 || len(file.notes) != 2 || file.notes[0].line != 1 || file.notes[1].line != 3 {
		t.Fatalf("the replay merges the file's notes again: files=%d notes=%+v", len(m.review.files), file.notes)
	}
}

func TestEditorQuitSavesTheReviewSession(t *testing.T) {
	m, _, history := startedReviewSession(t)
	m, waiter := sendReviewSession(m, sessionRecord("wait", "r1", "", ""))
	_, cmd := m.quit()
	if outcome := gotReply(t, waiter); !extmsg.IsEndedReply(outcome) {
		t.Fatalf("quitting the editor ends the session: %q", outcome)
	}
	cmd()
	if records, _ := history.LoadExternal(); len(records) != 1 || records[0].Session != "r1" {
		t.Fatalf("quitting the editor saves the session: %+v", records)
	}
}

func TestReceiverKeepsReviewSessionsOutOfHistory(t *testing.T) {
	_, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644))
	settings := store.NewMemoryExternalSettings(store.ExternalSettings{Enabled: true, HistoryLimit: 10})
	history := store.NewMemoryExternalHistory()
	receiver := NewExternalReceiver(root, settings, history)
	delivered := make(chan externalMessageMsg, 1)
	receiver.SetSink(func(msg any) { delivered <- msg.(externalMessageMsg) })

	replies := make(chan string, 1)
	go func() { replies <- receiver.Handle("review --review-session=r\na.go\n---\nno line needed\n") }()
	msg := <-delivered
	msg.reply <- "file 1 queued"
	if reply := <-replies; reply != "ok file 1 queued" || msg.sessionKind != extmsg.SessionReview || msg.record.LineNote != "no line needed" {
		t.Fatalf("a review session's file reaches the editor: %q %+v", reply, msg)
	}
	if records, _ := history.LoadExternal(); len(records) != 0 {
		t.Fatalf("a review session's file is not recorded on arrival: %+v", records)
	}
}

func TestHistoryDetailShowsEachFilesNote(t *testing.T) {
	m, _, history := startedReviewSession(t)
	m = key(m, keyPress(tea.KeyEscape))
	m = keySettled(m, keyPress(tea.KeyEnter)) // Quit
	records, _ := history.LoadExternal()
	m.externalHistory.records = records
	if plain := ansi.Strip(strings.Join(m.externalDetailLines(80), "\n")); !strings.Contains(plain, "apps/web/main.go#L3") || !strings.Contains(plain, "↳ Why …") {
		t.Fatalf("the detail shows the file's note:\n%s", plain)
	}
}

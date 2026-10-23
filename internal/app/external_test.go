package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/store"
)

// externalModel wires memory stores into a model; enabled sets the global
// switch.
func externalModel(t *testing.T, m Model, enabled bool) (Model, *store.MemoryExternalSettings, *store.MemoryExternalHistory) {
	t.Helper()
	settings := store.NewMemoryExternalSettings(store.ExternalSettings{Enabled: enabled, HistoryLimit: 10})
	history := store.NewMemoryExternalHistory()
	return m.WithExternal(settings, history, "/tmp/ntee.sock", ""), settings, history
}

func externalRecord(command string, paths ...string) store.ExternalRecord {
	return store.ExternalRecord{
		Key:        store.NewExternalRecordKey(time.Now()),
		Command:    command,
		Paths:      paths,
		ReceivedAt: time.Now(),
		Status:     store.ExternalPending,
	}
}

// receive delivers an external request as the socket would, returning the
// reply the sender got.
func receive(m Model, history store.ExternalHistoryStore, record store.ExternalRecord) (Model, tea.Cmd, string) {
	_ = history.AppendExternal(record, 10)
	reply := make(chan string, 1)
	m, cmd := deliver(m, externalMessageMsg{record: record, reply: reply})
	select {
	case outcome := <-reply:
		return m, cmd, outcome
	default:
		return m, cmd, ""
	}
}

func historyStatus(t *testing.T, history store.ExternalHistoryStore, key string) store.ExternalRecord {
	t.Helper()
	records, _ := history.LoadExternal()
	for _, record := range records {
		if record.Key == key {
			return record
		}
	}
	t.Fatalf("record %s not in history", key)
	return store.ExternalRecord{}
}

func TestExternalOpenFromQueryModeActsAtOnce(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m, _, history := externalModel(t, m, true)
	record := externalRecord("open", "main.go#L3")
	m, cmd, outcome := receive(m, history, record)
	m = settleExternal(m, cmd)
	if m.mode != modeEdit || m.openRel != "main.go" || m.edit.cy != 2 {
		t.Fatalf("open: mode=%v open=%q cy=%d", m.mode, m.openRel, m.edit.cy)
	}
	if m.choice.open() {
		t.Fatal("no confirmation is needed from the plain query view")
	}
	if !strings.Contains(outcome, "opened main.go#L3") {
		t.Fatalf("reply = %q", outcome)
	}
	if got := historyStatus(t, history, record.Key); got.Status != store.ExternalOpened {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestExternalOpenWhileEditingAsksFirst(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m, _, history := externalModel(t, m, true)
	m = m.openFileAt("lib/util.ts")
	m.edit.dirty = true

	record := externalRecord("open", "main.go#L2")
	m, _, outcome := receive(m, history, record)
	if !m.choice.open() || m.choice.kind != choiceExternalRequest || m.openRel != "lib/util.ts" {
		t.Fatalf("a busy user is asked first: choice=%v open=%q", m.choice.open(), m.openRel)
	}
	if outcome != "waiting for the user to confirm" {
		t.Fatalf("reply = %q", outcome)
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"External request · open", "main.go#L2", "unsaved edits in lib/util.ts are kept as a draft", "Jump", "Ignore"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("overlay missing %q", want)
		}
	}

	// Typing is swallowed by the overlay, never reaching the buffer.
	m = key(m, typeRune('x'))
	if !m.choice.open() {
		t.Fatal("stray keys keep the overlay open")
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleExternal(next.(Model), cmd)
	if m.mode != modeEdit || m.openRel != "main.go" || m.edit.cy != 1 {
		t.Fatalf("Jump opens the file on the line: mode=%v open=%q cy=%d", m.mode, m.openRel, m.edit.cy)
	}
	if got := historyStatus(t, history, record.Key); got.Status != store.ExternalOpened {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestExternalIgnoreAndSupersede(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m, _, history := externalModel(t, m, true)
	m = m.openFileAt("lib/util.ts")

	first := externalRecord("open", "main.go")
	m, _, _ = receive(m, history, first)
	second := externalRecord("review", "main.go")
	second.Key += "1" // arrives after first
	m, cmd, _ := receive(m, history, second)
	m = settleExternal(m, cmd)
	if got := historyStatus(t, history, first.Key); got.Status != store.ExternalSuperseded {
		t.Fatalf("an unanswered request is superseded by a newer one: %q", got.Status)
	}
	if m.external.pending == nil || m.external.pending.record.Key != second.Key || !strings.Contains(ansi.Strip(m.View().Content), "Review") {
		t.Fatal("the newer request is the one being asked about")
	}

	next, cmd := m.Update(keyPress(tea.KeyEscape))
	m = settleExternal(next.(Model), cmd)
	if m.choice.open() || m.mode != modeEdit || m.openRel != "lib/util.ts" {
		t.Fatalf("Esc ignores and leaves the user where they were: mode=%v open=%q", m.mode, m.openRel)
	}
	if got := historyStatus(t, history, second.Key); got.Status != store.ExternalIgnored {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestExternalOpenSwitchesToTheFilesRepo(t *testing.T) {
	m, _ := workspaceModel(t, nil)
	m, _, history := externalModel(t, m, true)
	m = enterRepo(t, m, "web")
	if m.activeRepo != "apps/web" {
		t.Fatalf("active repo = %q", m.activeRepo)
	}
	m = m.openFileAt("main.go")

	m, _, _ = receive(m, history, externalRecord("open", "libs/core/lib.go#L3"))
	if !strings.Contains(ansi.Strip(m.View().Content), "switches repo: apps/web/ → libs/core/") {
		t.Fatalf("the overlay must name the repo switch:\n%s", ansi.Strip(m.View().Content))
	}
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = settleExternal(next.(Model), cmd)
	if m.activeRepo != "libs/core" || m.openRel != "lib.go" || m.edit.cy != 2 || m.mode != modeEdit {
		t.Fatalf("after the jump: repo=%q open=%q cy=%d mode=%v err=%q", m.activeRepo, m.openRel, m.edit.cy, m.mode, m.errText)
	}
}

func TestExternalReceiverChecksSwitchAndPaths(t *testing.T) {
	m, root := newTestModel(t, nil)
	settings := store.NewMemoryExternalSettings(store.DefaultExternalSettings)
	history := store.NewMemoryExternalHistory()
	receiver := NewExternalReceiver(root, settings, history)

	if reply := receiver.Handle("open\nmain.go\n"); !strings.HasPrefix(reply, "error external messages are disabled") {
		t.Fatalf("off by default: %q", reply)
	}
	must(t, settings.SaveExternalSettings(store.ExternalSettings{Enabled: true, HistoryLimit: 10}))
	for _, text := range []string{"open\n../outside.go\n", "open\n/etc/hosts\n", "open\nmissing.go\n", "open\nlib\n", "bogus\nmain.go\n"} {
		if reply := receiver.Handle(text); !strings.HasPrefix(reply, "error ") {
			t.Fatalf("%q must be refused, got %q", text, reply)
		}
	}
	if records, _ := history.LoadExternal(); len(records) != 0 {
		t.Fatal("refused requests are not recorded")
	}

	// An accepted request is recorded with workspace-relative paths and
	// delivered to the UI, whose outcome is the reply.
	delivered := make(chan externalMessageMsg, 1)
	receiver.SetSink(func(msg any) { delivered <- msg.(externalMessageMsg) })
	replies := make(chan string, 1)
	go func() { replies <- receiver.Handle("open\n" + root + "/main.go#L2\n") }()
	msg := <-delivered
	if msg.record.Paths[0] != "main.go#L2" {
		t.Fatalf("absolute path resolved to %q", msg.record.Paths[0])
	}
	m, _ = deliver(m, msg)
	if reply := <-replies; reply != "ok opened main.go#L2 at line 2" {
		t.Fatalf("reply = %q", reply)
	}
	if records, _ := history.LoadExternal(); len(records) != 1 || records[0].Command != "open" {
		t.Fatalf("history = %+v", records)
	}
	if m.openRel != "main.go" {
		t.Fatalf("the UI opened %q", m.openRel)
	}
}

func TestExternalPanelTogglesAndLimits(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m, settings, history := externalModel(t, m, false)
	for i := 0; i < 6; i++ {
		record := externalRecord("open", "main.go")
		record.Key = store.NewExternalRecordKey(time.Unix(int64(i), 0))
		must(t, history.AppendExternal(record, 10))
	}

	next, cmd := m.Update(ctrlKey('t'))
	m = settleExternal(next.(Model), cmd)
	for m.inspectMenu != inspectMenuExternal {
		m = key(m, shiftKey(tea.KeyDown))
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"● listening", "off", "6 saved"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("panel missing %q:\n%s", want, plain)
		}
	}

	m = key(m, keyPress(tea.KeyRight)) // focus the panel
	if !m.externalPicker.focused {
		t.Fatal("→ focuses the panel")
	}
	next, cmd = m.Update(keyPress(tea.KeyEnter)) // toggle accept
	m = settleExternal(next.(Model), cmd)
	if got, _ := settings.LoadExternalSettings(); !got.Enabled || !m.external.settings.Enabled {
		t.Fatal("Enter on accept turns messages on")
	}

	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyEnter)) // edit the limit
	m = key(m, keyPress(tea.KeyBackspace))
	m = key(m, keyPress(tea.KeyBackspace))
	m = runes(m, "0")
	m = key(m, keyPress(tea.KeyEnter))
	if m.errText == "" || !m.externalPicker.editingLimit {
		t.Fatal("0 is out of range and keeps the input open")
	}
	m = key(m, keyPress(tea.KeyBackspace))
	m = runes(m, "3")
	next, cmd = m.Update(keyPress(tea.KeyEnter))
	m = settleExternal(next.(Model), cmd)
	if got, _ := settings.LoadExternalSettings(); got.HistoryLimit != 3 {
		t.Fatalf("limit = %d", got.HistoryLimit)
	}
	if records, _ := history.LoadExternal(); len(records) != 3 || m.external.savedCount != 3 {
		t.Fatalf("lowering the limit trims at once: %d saved, panel says %d", len(records), m.external.savedCount)
	}
}

func TestExternalHistoryViewAndReplay(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m, _, history := externalModel(t, m, true)
	older := externalRecord("open", "main.go#L2")
	older.Key = store.NewExternalRecordKey(time.Unix(1, 0))
	older.Status = store.ExternalOpened
	newer := externalRecord("review", "main.go", "lib/util.ts")
	newer.Key = store.NewExternalRecordKey(time.Unix(2, 0))
	newer.Args, newer.Base = []string{"v1.2.0"}, "v1.2.0"
	must(t, history.AppendExternal(older, 10))
	must(t, history.AppendExternal(newer, 10))

	next, cmd := m.Update(ctrlKey('t'))
	m = settleExternal(next.(Model), cmd)
	m, cmd = m.inspectExternalCommandForTest("history")
	m = settleExternal(m, cmd)
	if m.mode != modeExternalHistory || len(m.externalHistory.records) != 2 {
		t.Fatalf("history view: mode=%v records=%d", m.mode, len(m.externalHistory.records))
	}
	if m.externalHistory.records[0].Key != newer.Key {
		t.Fatal("newest first")
	}
	plain := ansi.Strip(m.View().Content)
	for _, want := range []string{"review", "2 files", "git hash", "v1.2.0", "lib/util.ts", "RAW MESSAGE"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("history view missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "review v1.2.0") {
		t.Fatalf("the base has its own row, not the command's:\n%s", plain)
	}

	m = key(m, keyPress(tea.KeyDown))
	if plain := ansi.Strip(m.View().Content); !strings.Contains(plain, "HEAD") {
		t.Fatalf("an open with no revision shows HEAD:\n%s", plain)
	}
	m = key(m, keyPress(tea.KeyEnter)) // replay the open
	if !m.choice.open() || m.external.pending == nil || !m.external.pending.replay {
		t.Fatal("a replay from the history view confirms first")
	}
	next, cmd = m.Update(keyPress(tea.KeyEnter))
	m = settleExternal(next.(Model), cmd)
	if m.mode != modeEdit || m.openRel != "main.go" {
		t.Fatalf("replay opened: mode=%v open=%q", m.mode, m.openRel)
	}
	if got := historyStatus(t, history, older.Key); got.Status != store.ExternalOpened {
		t.Fatal("a replay leaves the stored record alone")
	}
}

// inspectExternalCommandForTest runs an "ext …" bar command.
func (m Model) inspectExternalCommandForTest(args string) (Model, tea.Cmd) {
	next, cmd := m.inspectExternalCommand(args)
	return next.(Model), cmd
}

func TestExternalPanelInSingleFileModeShowsDisabled(t *testing.T) {
	m, root := newTestModel(t, nil)
	must(t, os.WriteFile(filepath.Join(root, ".env"), []byte("A=1\n"), 0o644))
	m, settings, _ := externalModel(t, m, true)
	m = m.WithOnlyFile(".env").WithStartFile(".env", 0)

	next, cmd := m.Update(ctrlKey('t'))
	m = settleExternal(next.(Model), cmd)
	for m.inspectMenu != inspectMenuExternal {
		m = key(m, shiftKey(tea.KeyDown))
	}
	plain := ansi.Strip(m.View().Content)
	if !strings.Contains(plain, "disabled") || !strings.Contains(plain, "single-file mode") {
		t.Fatalf("accept messages must read disabled:\n%s", plain)
	}

	m = key(m, keyPress(tea.KeyRight))
	next, cmd = m.Update(keyPress(tea.KeyEnter)) // try to toggle
	m = settleExternal(next.(Model), cmd)
	if got, _ := settings.LoadExternalSettings(); !got.Enabled || !strings.Contains(m.errText, "single-file mode") {
		t.Fatalf("the toggle is refused and the global switch untouched: enabled=%v err=%q", got.Enabled, m.errText)
	}
	m, _ = m.inspectExternalCommandForTest("off")
	if got, _ := settings.LoadExternalSettings(); !got.Enabled {
		t.Fatal("ext off is refused too")
	}
}

// settleDiff delivers the diff computed by cmd (dropped by settleExternal).
func settleDiff(m Model, cmd tea.Cmd) Model {
	for _, msg := range execCmds(cmd) {
		if ready, ok := msg.(diffReadyMsg); ok {
			m, _ = deliver(m, ready)
		}
	}
	return m
}

func TestExternalOpenWithABaseLandsInTheDiff(t *testing.T) {
	m, _ := gitReviewWorkspace(t)
	m, _ = deliver(m, workspaceReposMsg{repos: []string{"apps/web", "libs/core"}})
	m, _, history := externalModel(t, m, true)

	plain := externalRecord("open", "apps/web/main.go#L3")
	plain.Base = store.ExternalDefaultBase
	m, cmd, outcome := receive(m, history, plain)
	m = settleExternal(m, cmd)
	if m.mode != modeEdit || m.openRel != "apps/web/main.go" || m.edit.cy != 2 {
		t.Fatalf("HEAD is a plain jump: mode=%v open=%q cy=%d err=%q", m.mode, m.openRel, m.edit.cy, m.errText)
	}
	if strings.Contains(outcome, "diff") {
		t.Fatalf("reply = %q", outcome)
	}

	m = m.leaveToEditOrQuery()
	m.mode = modeQuery
	based := externalRecord("open", "apps/web/main.go#L3")
	based.Args, based.Base = []string{"HEAD~0"}, "HEAD~0"
	m, cmd, outcome = receive(m, history, based)
	if m.mode != modeDiff || m.diffBase != "HEAD~0" || m.openRel != "apps/web/main.go" {
		t.Fatalf("a named revision opens the diff: mode=%v base=%q open=%q err=%q", m.mode, m.diffBase, m.openRel, m.errText)
	}
	if !strings.Contains(outcome, "diff against HEAD~0") {
		t.Fatalf("reply = %q", outcome)
	}
	m = settleDiff(m, cmd)
	// The base is compared with the working file, whose uncommitted edit
	// changed line 3.
	if m.mode != modeDiff || m.diffDocument.adds != 1 || m.diffDocument.dels != 1 {
		t.Fatalf("diff against the working file: mode=%v +%d -%d err=%q", m.mode, m.diffDocument.adds, m.diffDocument.dels, m.errText)
	}
}

func TestExternalOpenWithABaseOutsideGitStillOpens(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m, _, history := externalModel(t, m, true)
	record := externalRecord("open", "main.go#L2")
	record.Args, record.Base = []string{"v1.2.0"}, "v1.2.0"
	m, cmd, _ := receive(m, history, record)
	m = settleExternal(m, cmd)
	if m.mode != modeEdit || m.openRel != "main.go" {
		t.Fatalf("the file still opens: mode=%v open=%q", m.mode, m.openRel)
	}
	got := historyStatus(t, history, record.Key)
	if got.Status != store.ExternalOpened || !strings.Contains(got.Note, "not in a git repository") {
		t.Fatalf("status = %q note = %q", got.Status, got.Note)
	}
}

func TestExternalBaseComesFromTheRecord(t *testing.T) {
	m, _ := newTestModel(t, nil)
	review := externalRecord("review", "main.go")
	for _, c := range []struct{ base, want string }{{"main", "main"}, {store.ExternalDefaultBase, ""}} {
		review.Base = c.base
		if pending, problem := m.resolveExternal(review); problem != "" || pending.base != c.want {
			t.Fatalf("base %q → %q (%s), want %q", c.base, pending.base, problem, c.want)
		}
	}
}

func TestExternalReceiverStoresTheBase(t *testing.T) {
	_, root := newTestModel(t, nil)
	settings := store.NewMemoryExternalSettings(store.ExternalSettings{Enabled: true, HistoryLimit: 10})
	history := store.NewMemoryExternalHistory()
	receiver := NewExternalReceiver(root, settings, history)
	delivered := make(chan externalMessageMsg, 1)
	receiver.SetSink(func(msg any) {
		message := msg.(externalMessageMsg)
		message.reply <- "done"
		delivered <- message
	})
	for _, c := range []struct{ text, want string }{
		{"open\nmain.go\n", "HEAD"},
		{"open a1b2c3d\nmain.go\n", "a1b2c3d"},
		{"review v1.2.0\nmain.go\n", "v1.2.0"},
	} {
		receiver.Handle(c.text)
		if got := (<-delivered).record.Base; got != c.want {
			t.Fatalf("%q → base %q, want %q", c.text, got, c.want)
		}
	}
	records, _ := history.LoadExternal()
	if len(records) != 3 || records[0].Base != "v1.2.0" || records[2].Base != "HEAD" {
		t.Fatalf("history bases: %+v", records)
	}
}

func TestExternalHistoryDetailTakesFocusToScroll(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m, _, history := externalModel(t, m, true)
	paths := make([]string, 120)
	for i := range paths {
		paths[i] = "dir/file" + strings.Repeat("x", i%5) + ".go"
	}
	older := externalRecord("open", "main.go")
	older.Key = store.NewExternalRecordKey(time.Unix(1, 0))
	big := externalRecord("review", paths...)
	big.Key = store.NewExternalRecordKey(time.Unix(2, 0))
	must(t, history.AppendExternal(older, 10))
	must(t, history.AppendExternal(big, 10))
	m, cmd := m.enterExternalHistory()
	m = settleExternal(m, cmd)
	if span := m.externalDetailSpan(); span.total <= span.height {
		t.Fatalf("fixture: the details must overflow (%d lines, %d high)", span.total, span.height)
	}

	m = key(m, keyPress(tea.KeyDown))
	if m.externalHistory.selection.index != 1 || m.externalHistory.detail.scrollY != 0 {
		t.Fatal("with the list focused ↓ picks the next message")
	}
	m = key(m, keyPress(tea.KeyUp))
	m = key(m, keyPress(tea.KeyRight))
	if !m.externalHistory.detailFocused {
		t.Fatal("→ focuses the details")
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "← back to the list") {
		t.Fatal("the focused pane says so")
	}
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyDown))
	if m.externalHistory.detail.scrollY != 2 || m.externalHistory.selection.index != 0 {
		t.Fatalf("↓ scrolls the details a line at a time: scroll=%d selection=%d", m.externalHistory.detail.scrollY, m.externalHistory.selection.index)
	}
	m = key(m, keyPress(tea.KeyEnd))
	span := m.externalDetailSpan()
	if m.externalHistory.detail.scrollY != span.total-span.height {
		t.Fatal("End reaches the last path")
	}
	m = key(m, keyPress(tea.KeyDown))
	if m.externalHistory.detail.scrollY != span.total-span.height {
		t.Fatal("scrolling stops at the end")
	}

	m = m.wheelScroll(-1)
	if m.externalHistory.detail.scrollY != span.total-span.height-wheelScrollLines || m.externalHistory.selection.index != 0 {
		t.Fatal("the wheel scrolls the focused details")
	}

	m = key(m, keyPress(tea.KeyEscape))
	if m.externalHistory.detailFocused || m.mode != modeExternalHistory {
		t.Fatal("Esc in the details returns to the list, not out of the view")
	}
	m = key(m, keyPress(tea.KeyEscape))
	if m.mode != modeInspect {
		t.Fatal("Esc in the list leaves the history view")
	}
}

package app

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nickooan/ntee-editor/internal/store"
)

// opModeFixture is a model with main.go open and the given op-commands saved.
func opModeFixture(t *testing.T, commands ...store.OpCommand) (Model, string) {
	t.Helper()
	commandStore := store.NewMemoryOpCommands()
	for _, command := range commands {
		must(t, commandStore.PutOpCommand("", command))
	}
	m, root := newTestModel(t, nil)
	m = m.WithOpCommandStore(commandStore).openFileAt("main.go")
	return m, root
}

func TestOpModeRequiresOpenFile(t *testing.T) {
	m, _ := newTestModel(t, nil)
	m = key(m, ctrlKey('r'))
	if m.opMode.open || m.errText != "open a file to run op-commands" {
		t.Fatalf("Ctrl+R without a file: open=%v err=%q", m.opMode.open, m.errText)
	}
}

func TestOpModeBlockedInBars(t *testing.T) {
	m, _ := opModeFixture(t)
	m = press(t, m, ctrlKey('t'))
	m = key(m, ctrlKey('r'))
	if m.opMode.open {
		t.Fatal("Ctrl+R must not open from the inspection bar")
	}
}

func TestOpModePickArgsRunFlow(t *testing.T) {
	m, root := opModeFixture(t,
		store.OpCommand{Name: "build", Command: "make"},
		store.OpCommand{Name: "echo-args", Command: "printf '%s\\n' {$1} {$fpath}"},
	)
	m = press(t, m, ctrlKey('r'))
	if !m.opMode.open || len(m.opMode.matches) != 2 {
		t.Fatalf("overlay should list both commands: open=%v matches=%d", m.opMode.open, len(m.opMode.matches))
	}
	m = runes(m, "echo")
	if len(m.opMode.matches) != 1 {
		t.Fatalf("query should filter to one command, got %d", len(m.opMode.matches))
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.opMode.stage != opStageArgs {
		t.Fatalf("a template with {$1} should ask for args, stage=%v", m.opMode.stage)
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.opMode.stage != opStagePick || !m.opMode.open || m.opMode.query != "echo" {
		t.Fatal("Esc in args should return to the picker with the query kept")
	}
	m = key(m, keyPress(tea.KeyEnter))

	m = key(m, keyPress(tea.KeyEnter)) // no args yet
	if m.opMode.stage != opStageArgs || m.errText != "needs 1 args, got 0" {
		t.Fatalf("missing args should keep the args stage: stage=%v err=%q", m.opMode.stage, m.errText)
	}
	m = runes(m, `"hello world"`)
	filePath := filepath.Join(root, "main.go")
	if commandLine, err := m.renderOpCommandLine(); err != nil || commandLine != "printf '%s\\n' 'hello world' "+filePath {
		t.Fatalf("rendered command = %q, %v", commandLine, err)
	}
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$ printf '%s\\n' 'hello world'") {
		t.Fatalf("args stage should preview the rendered command:\n%s", frame)
	}

	m = press(t, m, keyPress(tea.KeyEnter))
	run := m.opMode.run
	if m.opMode.stage != opStageRun || run == nil || !run.finished {
		t.Fatalf("Enter should run to completion: stage=%v run=%+v", m.opMode.stage, run)
	}
	if want := []string{"hello world", filePath}; !slices.Equal(run.output.displayLines(), want) {
		t.Fatalf("output = %q, want %q", run.output.displayLines(), want)
	}
	if run.exitCode != 0 || run.err != nil {
		t.Fatalf("exit=%d err=%v", run.exitCode, run.err)
	}
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "finished (exit 0)") || !strings.Contains(frame, "hello world") {
		t.Fatalf("run view should show output and the done line:\n%s", frame)
	}
	m = key(m, keyPress(tea.KeyEsc))
	if m.opMode.open {
		t.Fatal("Esc after the run should close the overlay")
	}
}

func TestOpModeZeroArgCommandRunsImmediately(t *testing.T) {
	m, root := opModeFixture(t, store.OpCommand{Name: "lines", Command: "printf 'a\\nb\\n'; pwd"})
	m = press(t, m, ctrlKey('r'))
	m = press(t, m, keyPress(tea.KeyEnter))
	run := m.opMode.run
	if m.opMode.stage != opStageRun || run == nil || !run.finished {
		t.Fatalf("zero-arg command should run straight away: stage=%v", m.opMode.stage)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	must(t, err)
	lines := run.output.displayLines()
	if len(lines) != 3 || lines[0] != "a" || lines[1] != "b" {
		t.Fatalf("output = %q", lines)
	}
	if lines[2] != root && lines[2] != resolvedRoot {
		t.Fatalf("command should run in the project root, pwd=%q", lines[2])
	}
}

func TestOpModeFailureStatus(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "fail", Command: "echo oops >&2; exit 3"})
	m = press(t, m, ctrlKey('r'))
	m = press(t, m, keyPress(tea.KeyEnter))
	run := m.opMode.run
	if !run.finished || run.exitCode != 3 {
		t.Fatalf("exit code = %d finished=%v", run.exitCode, run.finished)
	}
	if text, ok := run.status(); ok || !strings.Contains(text, "exited with status 3") {
		t.Fatalf("status = %q ok=%v", text, ok)
	}
	if lines := run.output.displayLines(); !slices.Equal(lines, []string{"oops"}) {
		t.Fatalf("stderr should be captured: %q", lines)
	}
}

func TestOpModeEscCancelsRunningCommand(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "slow", Command: "sleep 30; echo late"})
	m = press(t, m, ctrlKey('r'))
	next, cmd := m.Update(keyPress(tea.KeyEnter))
	m = next.(Model)
	if m.opMode.stage != opStageRun || cmd == nil {
		t.Fatal("Enter should start the run")
	}
	messages := make(chan tea.Msg, 1)
	go func() { messages <- cmd() }()

	gen := m.opRunGen
	m = key(m, keyPress(tea.KeyEsc))
	if m.opMode.open {
		t.Fatal("Esc while running should close the overlay")
	}
	select {
	case msg := <-messages:
		runMsg, ok := msg.(opRunMsg)
		if !ok || !runMsg.done || runMsg.gen != gen {
			t.Fatalf("expected the cancelled run's final message, got %#v", msg)
		}
		m, _ = deliver(m, msg)
		if m.opMode.open || m.opMode.run != nil {
			t.Fatal("a cancelled run's message must be dropped")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled command did not stop")
	}
}

func TestOpModeStaleRunMessageIgnored(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "lines", Command: "printf 'a\\n'"})
	m = press(t, m, ctrlKey('r'))
	m = press(t, m, keyPress(tea.KeyEnter))
	m, cmd := deliver(m, opRunMsg{gen: m.opRunGen - 1, opRunEvent: opRunEvent{data: []byte("stale\n")}})
	if cmd != nil || slices.Contains(m.opMode.run.output.displayLines(), "stale") {
		t.Fatal("output from an older run must be dropped")
	}
}

func TestOpModeEmptyStateRenders(t *testing.T) {
	m, _ := opModeFixture(t)
	m = press(t, m, ctrlKey('r'))
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "no op-commands") {
		t.Fatalf("empty overlay should point at Ctrl+T:\n%s", frame)
	}
	m = key(m, keyPress(tea.KeyEnter)) // nothing to select
	if m.opMode.stage != opStagePick {
		t.Fatal("Enter with no matches must stay in the picker")
	}
}

func TestOpRunOutputLineHandling(t *testing.T) {
	var output opRunOutput
	output.write([]byte("one\r\ntwo\r"))
	output.write([]byte("\nprogress 10%\rprogress 100%\n\x1b[32mgreen\x1b[0m\npartial"))
	want := []string{"one", "two", "progress 100%", "green", "partial"}
	if got := output.displayLines(); !slices.Equal(got, want) {
		t.Fatalf("lines = %q, want %q", got, want)
	}
}

func TestOpRunOutputBoundsLines(t *testing.T) {
	var output opRunOutput
	output.write([]byte(strings.Repeat("x\n", opRunMaxLines*2)))
	if count := len(output.displayLines()); count < opRunMaxLines || count > opRunMaxLines+opRunMaxLines/4 {
		t.Fatalf("retained %d lines, want about %d", count, opRunMaxLines)
	}
}

func TestOpRunScrollAndFollow(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "many", Command: "seq 1 100"})
	m = press(t, m, ctrlKey('r'))
	m = press(t, m, keyPress(tea.KeyEnter))
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "100") {
		t.Fatalf("run view should follow the tail:\n%s", frame)
	}
	m = key(m, keyPress(tea.KeyHome))
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "End follow") || !strings.Contains(frame, "│ 1 ") || !strings.Contains(frame, "│ 2 ") {
		t.Fatalf("scrolled view should offer to follow again:\n%s", frame)
	}
	m = key(m, keyPress(tea.KeyEnd))
	if m.opMode.run.scroll != 0 {
		t.Fatal("End should follow the tail again")
	}
}

package app

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
	m, _ := opModeFixture(t,
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
	filePath := "main.go" // workspace-relative
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

func TestOpModeSystemPathsAreWorkspaceRelative(t *testing.T) {
	m, root := opModeFixture(t, store.OpCommand{Name: "paths", Command: "printf '%s\\n' {$fpath} {$dpath}"})
	m = m.openFileAt("lib/util.ts")
	m = press(t, m, ctrlKey('r'))
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"lib/util.ts", "lib"}) {
		t.Fatalf("nested file: %q", lines)
	}
	m = key(m, keyPress(tea.KeyEsc))
	m = m.openFileAt("main.go")
	m = press(t, m, ctrlKey('r'))
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"main.go", "."}) {
		t.Fatalf("top-level file in %s: %q", root, lines)
	}
}

func TestOpModeRunsFromWorkspaceInsideRepo(t *testing.T) {
	commandStore := store.NewMemoryOpCommands()
	must(t, commandStore.PutOpCommand("", store.OpCommand{Name: "where", Command: "printf '%s\\n' {$fpath} {$dpath}; pwd; test -f {$fpath} && echo found"}))
	m, root := workspaceModel(t, nil)
	m = enterRepo(t, m, "web")
	if m.activeRepo != "apps/web" {
		t.Fatalf("activeRepo = %q", m.activeRepo)
	}
	m = m.WithOpCommandStore(commandStore).openFileAt("main.go")
	m = press(t, m, ctrlKey('r'))
	m = press(t, m, keyPress(tea.KeyEnter))

	resolvedRoot, err := filepath.EvalSymlinks(root)
	must(t, err)
	lines := m.opMode.run.output.displayLines()
	if len(lines) != 4 || lines[0] != "apps/web/main.go" || lines[1] != "apps/web" || lines[3] != "found" {
		t.Fatalf("paths should be workspace-relative and resolve from the cwd: %q", lines)
	}
	if lines[2] != root && lines[2] != resolvedRoot {
		t.Fatalf("command should run in the workspace %q, not the repo; pwd=%q", root, lines[2])
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

func TestOpModeInlineArgsRunDirectly(t *testing.T) {
	m, root := opModeFixture(t, store.OpCommand{Name: "test", Command: "cat {$fpath} | tail -n {$1}"})
	var content strings.Builder
	for line := 1; line <= 20; line++ {
		content.WriteString(strconv.Itoa(line) + "\n")
	}
	must(t, os.WriteFile(filepath.Join(root, "lines.txt"), []byte(content.String()), 0o644))
	m = m.openFileAt("lines.txt")
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "test 3")
	if len(m.opMode.matches) != 1 {
		t.Fatalf("the name part should still match the command, got %d matches", len(m.opMode.matches))
	}
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$ cat lines.txt | tail -n 3") {
		t.Fatalf("picker should preview the command Enter will run:\n%s", frame)
	}
	m = press(t, m, keyPress(tea.KeyEnter))
	run := m.opMode.run
	if m.opMode.stage != opStageRun || run == nil || !run.finished || run.exitCode != 0 {
		t.Fatalf("inline args should run straight away: stage=%v run=%+v", m.opMode.stage, run)
	}
	if lines := run.output.displayLines(); !slices.Equal(lines, []string{"18", "19", "20"}) {
		t.Fatalf("output = %q", lines)
	}
}

func TestOpModeInlineArgsQuoting(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "echo-args", Command: "printf '%s\\n' {$1}"})
	m = press(t, m, ctrlKey('r'))
	m = runes(m, `echo "hello world"`)
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"hello world"}) {
		t.Fatalf("a quoted inline arg should stay one argument: %q", lines)
	}
}

func TestOpModeInlineArgsWrongCountOpensArgsStage(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "pair", Command: "echo {$1} {$2}"})
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "pair one")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "needs 2 args, got 1") {
		t.Fatalf("picker should show why the inline args don't fit:\n%s", frame)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.opMode.stage != opStageArgs || m.opMode.args != "one" || m.opMode.argsCursor != 3 {
		t.Fatalf("args stage should open pre-filled: stage=%v args=%q cursor=%d", m.opMode.stage, m.opMode.args, m.opMode.argsCursor)
	}
	m = runes(m, " two")
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"one two"}) {
		t.Fatalf("output = %q", lines)
	}
}

func TestOpModeTypingArgsKeepsSelection(t *testing.T) {
	m, _ := opModeFixture(t,
		store.OpCommand{Name: "build", Command: "make {$1}"},
		store.OpCommand{Name: "bump", Command: "echo {$1}"},
	)
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "b")
	m = key(m, keyPress(tea.KeyDown))
	selected, _ := m.opHighlighted()
	m = runes(m, " x")
	if after, _ := m.opHighlighted(); after.Name != selected.Name || m.opMode.index != 1 {
		t.Fatalf("typing args should keep the chosen row: before=%q after=%q index=%d", selected.Name, after.Name, m.opMode.index)
	}
	m = key(m, keyPress(tea.KeyBackspace))
	m = key(m, keyPress(tea.KeyBackspace))
	m = runes(m, "u")
	if m.opMode.index != 0 {
		t.Fatal("changing the name part should re-filter and reset the selection")
	}
}

func TestOpQueryParts(t *testing.T) {
	cases := []struct{ query, name, args string }{
		{"test", "test", ""},
		{"test 10", "test", "10"},
		{"  test   a  b ", "test", "a  b"},
		{"test ", "test", ""},
		{"", "", ""},
	}
	for _, tc := range cases {
		if name, args := opQueryParts(tc.query); name != tc.name || args != tc.args {
			t.Errorf("opQueryParts(%q) = %q %q, want %q %q", tc.query, name, args, tc.name, tc.args)
		}
	}
}

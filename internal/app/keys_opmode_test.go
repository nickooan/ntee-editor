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
	if m.opMode.stage != opStageArgs || m.errText != "needs 1 arg, got 0" {
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

func TestOpModeDefaultsFillOmittedArgs(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "pair", Command: `printf '%s|%s\n' {$1} {$2 or "x y"}`})
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "pair a")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$ printf '%s|%s\\n' a 'x y'") {
		t.Fatalf("the preview should fill the default:\n%s", frame)
	}
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"a|x y"}) {
		t.Fatalf("output = %q", lines)
	}

	m = key(m, keyPress(tea.KeyEsc))
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "pair a b")
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"a|b"}) {
		t.Fatalf("a typed arg should override the default: %q", lines)
	}
}

func TestOpModeOptionalOnlyTemplateAsksThenUsesDefaults(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "log", Command: `printf '[%s]\n' {$1 or ""} -n {$2 or 5}`})
	m = press(t, m, ctrlKey('r'))
	m = key(m, keyPress(tea.KeyEnter))
	if m.opMode.stage != opStageArgs {
		t.Fatal("optional args should still get the args stage")
	}
	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, `$1 (omitted) · $2 5 (default)`) || !strings.Contains(frame, "$ printf '[%s]\\n'  -n 5") {
		t.Fatalf("args stage should show the defaults and the filled-in command:\n%s", frame)
	}
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"[-n]", "[5]"}) {
		t.Fatalf("an empty default must disappear, not pass '': %q", lines)
	}
}

func TestOpModeArgSpecsMarkRequired(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "cp", Command: `cp {$1} {$2 or /tmp}`})
	m = press(t, m, ctrlKey('r'))
	m = key(m, keyPress(tea.KeyEnter))
	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, "$1 required · $2 = /tmp") || !strings.Contains(frame, "needs 1 arg ($1), got 0") ||
		!strings.Contains(frame, "type 1 value for $1, or up to 2 to fill $1…$2 in order") {
		t.Fatalf("args stage should explain the arguments:\n%s", frame)
	}
}

// fourArgTemplate is the case from the request: $2 and $4 have defaults.
const fourArgTemplate = `printf '[%s]\n' {$1} {$2 or master} {$3} {$4 or ""}`

func TestOpModeRequiredOnlyArgsSkipDefaults(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "t4", Command: fourArgTemplate})
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "t4 a b")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$ printf '[%s]\\n' a master b ") {
		t.Fatalf("two values should fill $1 and $3, with $2/$4 defaulted:\n%s", frame)
	}
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"[a]", "[master]", "[b]"}) {
		t.Fatalf("output = %q", lines)
	}
}

func TestOpModeArgsStageShowsBinding(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "t4", Command: fourArgTemplate})
	m.width = 180 // wide enough that each asserted line renders unwrapped
	m = press(t, m, ctrlKey('r'))
	m = key(m, keyPress(tea.KeyEnter))
	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, `$1 required · $2 = master · $3 required · $4 = "" (omitted)`) ||
		!strings.Contains(frame, "type 2 values for $1 $3, or up to 4 to fill $1…$4 in order") {
		t.Fatalf("the args stage should explain both ways to call it:\n%s", frame)
	}

	m = runes(m, "a")
	frame = ansi.Strip(m.render())
	if !strings.Contains(frame, "needs 2 args ($1, $3), got 1") {
		t.Fatalf("one value is too few:\n%s", frame)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.opMode.stage != opStageArgs {
		t.Fatal("too few args must keep the args stage")
	}

	m = runes(m, " b")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$1 a · $2 master (default) · $3 b · $4 (omitted)") {
		t.Fatalf("required-only binding should be shown per argument:\n%s", frame)
	}
	m = runes(m, " c")
	frame = ansi.Strip(m.render())
	if !strings.Contains(frame, "$1 a · $2 b · $3 c · $4 (omitted)") || !strings.Contains(frame, "$ printf '[%s]\\n' a b c ") {
		t.Fatalf("a third value switches to filling in order:\n%s", frame)
	}
}

func TestOpModeUncoveredRequiredArg(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "gap", Command: `echo {$1} {$2 or p} {$3 or q} {$4}`})
	m.width = 180 // wide enough that each asserted line renders unwrapped
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "gap a b c")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$4 has no default") {
		t.Fatalf("an uncovered required arg should be reported in the preview:\n%s", frame)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if m.opMode.stage != opStageArgs || m.opMode.args != "a b c" {
		t.Fatalf("Enter should open the args stage pre-filled: stage=%v args=%q", m.opMode.stage, m.opMode.args)
	}
}

func TestOpModeArgLinesWrapInsteadOfCutting(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "t4", Command: fourArgTemplate})
	m = press(t, m, ctrlKey('r'))
	m = key(m, keyPress(tea.KeyEnter))
	frame := ansi.Strip(m.render())
	for _, want := range []string{`$4 = "" (omitted)`, "│ order ", "needs 2 args ($1, $3), got 0"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("narrow args box should wrap, keeping %q whole:\n%s", want, frame)
		}
	}
}

func TestOpModeQueryCursorEditing(t *testing.T) {
	m, _ := opModeFixture(t,
		store.OpCommand{Name: "build", Command: "make"},
		store.OpCommand{Name: "test", Command: "printf '%s\\n' {$1}"},
	)
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "tst 10")
	for range 5 {
		m = key(m, keyPress(tea.KeyLeft))
	}
	if m.opMode.queryCursor != 1 {
		t.Fatalf("← should move the cursor back to after 't', cursor=%d", m.opMode.queryCursor)
	}
	m = runes(m, "e")
	if m.opMode.query != "test 10" || m.opMode.queryCursor != 2 {
		t.Fatalf("typing should insert at the cursor: query=%q cursor=%d", m.opMode.query, m.opMode.queryCursor)
	}
	if command, ok := m.opHighlighted(); !ok || command.Name != "test" {
		t.Fatalf("the fixed name should re-filter to test, got %+v %v", command, ok)
	}
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$ printf '%s\\n' 10") {
		t.Fatalf("the inline preview should follow the edited query:\n%s", frame)
	}

	m = key(m, keyPress(tea.KeyHome))
	m = key(m, keyPress(tea.KeyDelete))
	m = key(m, keyPress(tea.KeyEnd))
	m = key(m, keyPress(tea.KeyBackspace))
	if m.opMode.query != "est 1" || m.opMode.queryCursor != 5 {
		t.Fatalf("Home+Delete and End+Backspace: query=%q cursor=%d", m.opMode.query, m.opMode.queryCursor)
	}
	m = key(m, ctrlKey('a'))
	m = key(m, keyPress(tea.KeyRight))
	if m.opMode.queryCursor != 1 {
		t.Fatalf("Ctrl+A then → should land after the first rune, cursor=%d", m.opMode.queryCursor)
	}
	m = key(m, ctrlKey('e'))
	if m.opMode.queryCursor != 5 {
		t.Fatalf("Ctrl+E should jump to the end, cursor=%d", m.opMode.queryCursor)
	}
}

func TestOpModeQueryCursorDrawnInPlace(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "build", Command: "make"})
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "bld")
	m = key(m, keyPress(tea.KeyLeft))
	if frame := m.render(); !strings.Contains(frame, cursorStyle.Render("d")) {
		t.Fatal("the cursor block should sit on 'd', not after the query")
	}
}

func TestOpModeEditingArgsInQueryKeepsSelection(t *testing.T) {
	m, _ := opModeFixture(t,
		store.OpCommand{Name: "echo-a", Command: "echo {$1}"},
		store.OpCommand{Name: "echo-b", Command: "echo {$1}"},
	)
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "echo 12")
	m = key(m, keyPress(tea.KeyDown))
	m = key(m, keyPress(tea.KeyLeft))
	m = runes(m, "x")
	if m.opMode.query != "echo 1x2" || m.opMode.index != 1 {
		t.Fatalf("inserting into the args part must keep the row: query=%q index=%d", m.opMode.query, m.opMode.index)
	}
}

func TestOpModePasteInsertsAtQueryCursor(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "test", Command: "echo {$1}"})
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "test  b")
	m = key(m, keyPress(tea.KeyLeft))
	m = key(m, keyPress(tea.KeyLeft))
	next, _ := m.Update(tea.PasteMsg{Content: "a"})
	m = next.(Model)
	if m.opMode.query != "test a b" || m.opMode.queryCursor != 6 {
		t.Fatalf("paste should insert at the cursor: query=%q cursor=%d", m.opMode.query, m.opMode.queryCursor)
	}
}

func TestOpModeArgsDeleteAndEnd(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "test", Command: "echo {$1} {$2}"})
	m = press(t, m, ctrlKey('r'))
	m = key(m, keyPress(tea.KeyEnter))
	m = runes(m, "ab cd")
	m = key(m, keyPress(tea.KeyHome))
	m = key(m, keyPress(tea.KeyDelete))
	m = key(m, ctrlKey('e'))
	m = runes(m, "e")
	if m.opMode.args != "b cde" || m.opMode.argsCursor != 5 {
		t.Fatalf("args Delete/Ctrl+E: args=%q cursor=%d", m.opMode.args, m.opMode.argsCursor)
	}
}

func TestOpModeShowsTargetFile(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "echo", Command: "echo {$1}"})
	m = press(t, m, ctrlKey('r'))
	frame := ansi.Strip(m.render())
	if !strings.Contains(frame, "file main.go") || strings.Contains(frame, "unsaved changes") {
		t.Fatalf("the picker should name the clean target file:\n%s", frame)
	}
	m = key(m, keyPress(tea.KeyEnter))
	if frame := ansi.Strip(m.render()); m.opMode.stage != opStageArgs || !strings.Contains(frame, "file main.go") {
		t.Fatalf("the args stage should name the target file:\n%s", frame)
	}
	m = runes(m, "hi")
	m = press(t, m, keyPress(tea.KeyEnter))
	if frame := ansi.Strip(m.render()); m.opMode.stage != opStageRun || !strings.Contains(frame, "── main.go ─") {
		t.Fatalf("the run divider should name the target file:\n%s", frame)
	}
}

func TestOpModeWarnsUnsavedEdits(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "build", Command: "true"})
	m = runes(m, "x") // fixture opens main.go in edit mode
	m = press(t, m, ctrlKey('r'))
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "unsaved changes — the command reads the saved file") {
		t.Fatalf("the picker should warn about unsaved edits:\n%s", frame)
	}
	m = press(t, m, keyPress(tea.KeyEnter))
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "main.go · unsaved edits not included") {
		t.Fatalf("the run divider should say the edits were not included:\n%s", frame)
	}

	m = key(m, keyPress(tea.KeyEsc))
	m = press(t, m, ctrlKey('s'))
	m = press(t, m, ctrlKey('r'))
	if frame := ansi.Strip(m.render()); m.opTargetUnsaved() || strings.Contains(frame, "unsaved changes") {
		t.Fatalf("after saving the warning should be gone:\n%s", frame)
	}
}

func TestOpRunTargetDividerFitsWidth(t *testing.T) {
	run := &opRunState{target: "internal/some/deeply/nested/package/main.go", unsaved: true}
	for _, width := range []int{80, 50, 20, 6} {
		divider := ansi.Strip(renderOpRunTargetDivider(run, width))
		if got := len([]rune(divider)); got != width {
			t.Fatalf("width %d: divider is %d wide: %q", width, got, divider)
		}
		if width >= 50 && !strings.Contains(divider, "main.go · unsaved edits not included") {
			t.Fatalf("width %d: the file name should survive a left cut: %q", width, divider)
		}
	}
	if got := truncateRunesLeft("a/b/main.go", 8); got != "…main.go" {
		t.Fatalf("truncateRunesLeft = %q", got)
	}
}

func TestOpModeOffersCommandsForTheOpenFileType(t *testing.T) {
	m, _ := opModeFixture(t,
		store.OpCommand{Name: "gotest", FileTypes: "go", Command: "go test ./..."},
		store.OpCommand{Name: "rbtest", FileTypes: "rb", Command: "rspec"},
		store.OpCommand{Name: "all", FileTypes: "*", Command: "ls"},
		store.OpCommand{Name: "legacy", Command: "pwd"},
	)
	m = press(t, m, ctrlKey('r'))
	var names []string
	for _, command := range m.opMode.candidates {
		names = append(names, command.Name)
	}
	if strings.Join(names, " ") != "all gotest legacy" {
		t.Fatalf("main.go should be offered go and * commands only, got %v", names)
	}
}

func TestOpModeSpecificTypeOverridesAll(t *testing.T) {
	m, _ := opModeFixture(t,
		store.OpCommand{Name: "test", FileTypes: "*", Command: "printf 'any:%s\\n' {$1}"},
		store.OpCommand{Name: "test", FileTypes: "go,sh", Command: "printf 'go:%s\\n' {$1}"},
	)
	m = press(t, m, ctrlKey('r'))
	if len(m.opMode.candidates) != 1 || m.opMode.candidates[0].FileTypes != "go,sh" {
		t.Fatalf("test for go should replace test for *: %+v", m.opMode.candidates)
	}
	m = runes(m, "test 10")
	m = press(t, m, keyPress(tea.KeyEnter))
	if run := m.opMode.run; run == nil || !slices.Equal(run.output.displayLines(), []string{"go:10"}) {
		t.Fatalf("the inline run should use the go command: %+v", run)
	}
}

func TestOpModeHintsWhenNoCommandMatchesTheFileType(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "rbtest", FileTypes: "rb", Command: "rspec"})
	m = press(t, m, ctrlKey('r'))
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "no op-commands for go files") {
		t.Fatalf("the picker should explain why nothing is listed:\n%s", frame)
	}
}

func TestOpModeExtraArgsAfterDoubleDash(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "p", Command: `printf '[%s]\n' {$1}`})
	m.width = 180 // wide enough that each asserted line renders unwrapped
	m = press(t, m, ctrlKey('r'))
	m = runes(m, `p a -- -x "b c"`)
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, `$ printf '[%s]\n' a -x 'b c'`) {
		t.Fatalf("words after -- should be appended, quoted:\n%s", frame)
	}
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"[a]", "[-x]", "[b c]"}) {
		t.Fatalf("output = %q", lines)
	}
}

func TestOpModeZeroArgCommandTakesExtraArgs(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "p", Command: `printf '[%s]\n' x`})
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "p -- y")
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"[x]", "[y]"}) {
		t.Fatalf("output = %q", lines)
	}
}

func TestOpModeFragmentArg(t *testing.T) {
	m, _ := opModeFixture(t, store.OpCommand{Name: "f", Command: `printf '[%s]\n' {$1} {#"-P $2"}`})
	m.width = 180 // wide enough that each asserted line renders unwrapped
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "f")
	m = key(m, keyPress(tea.KeyEnter))
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$1 required · $2 optional: -P $2") {
		t.Fatalf("the args stage should describe the fragment:\n%s", frame)
	}

	m = runes(m, "a")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$1 a · $2 (omitted)") || !strings.Contains(frame, `$ printf '[%s]\n' a `) {
		t.Fatalf("an omitted fragment arg should drop the whole fragment:\n%s", frame)
	}
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"[a]"}) {
		t.Fatalf("output = %q", lines)
	}

	m = key(m, keyPress(tea.KeyEsc))
	m = press(t, m, ctrlKey('r'))
	m = runes(m, "f")
	m = key(m, keyPress(tea.KeyEnter))
	m = runes(m, "a 3 -- -v")
	if frame := ansi.Strip(m.render()); !strings.Contains(frame, "$1 a · $2 3 · -- -v (appended)") {
		t.Fatalf("the args stage should show the appended extras:\n%s", frame)
	}
	m = press(t, m, keyPress(tea.KeyEnter))
	if lines := m.opMode.run.output.displayLines(); !slices.Equal(lines, []string{"[a]", "[-P]", "[3]", "[-v]"}) {
		t.Fatalf("output = %q", lines)
	}
}

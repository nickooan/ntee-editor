package app

import (
	"path"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nickooan/ntee-editor/internal/fuzzy"
	"github.com/nickooan/ntee-editor/internal/input"
	"github.com/nickooan/ntee-editor/internal/opcmd"
	"github.com/nickooan/ntee-editor/internal/store"
)

type opStage int

const (
	opStagePick opStage = iota
	opStageArgs
	opStageRun
)

const opRunPageLines = 10

// opModeState is the Ctrl+R operation overlay: pick a saved op-command, fill
// its {$n} args, then watch it run. The pick query is "name [args…]": the
// first word filters the commands, the rest are inline args for the pick.
type opModeState struct {
	open         bool
	stage        opStage
	query        string
	filteredName string // the name part matches were last computed for
	index        int
	corpus       []fuzzy.Prepared // aligned with candidates
	candidates   []store.OpCommand
	matches      []fuzzy.Match
	selected     store.OpCommand
	template     opcmd.Template
	args         string
	argsCursor   int
	run          *opRunState
}

// openOpMode opens the operation overlay. Commands render from the cached
// list at once; a reload refreshes them when it lands.
func (m Model) openOpMode() (tea.Model, tea.Cmd) {
	if m.openFile == nil {
		m.errText = "open a file to run op-commands"
		return m, nil
	}
	m = m.closeCompletion()
	m.opMode = opModeState{open: true}
	m = m.refreshOpMatches()
	return m.loadOpCommandsCmd()
}

// closeOpMode cancels a still-running command and resets the overlay. The run
// generation is bumped so the cancelled run's last messages are dropped.
func (m Model) closeOpMode() Model {
	if m.opMode.run != nil {
		m.opMode.run.cancel()
	}
	m.opRunGen++
	m.opMode = opModeState{}
	return m
}

// opQueryParts splits the pick query into the command name to filter on and
// the inline args that follow the first space.
func opQueryParts(query string) (name, inlineArgs string) {
	name, inlineArgs, _ = strings.Cut(strings.TrimLeft(query, " "), " ")
	return name, strings.TrimSpace(inlineArgs)
}

func (m Model) refreshOpMatches() Model {
	m.opMode.candidates = m.opCommands
	names := make([]string, len(m.opCommands))
	for index, command := range m.opCommands {
		names[index] = command.Name
	}
	m.opMode.corpus = fuzzy.Prepare(names)
	m.opMode.filteredName, _ = opQueryParts(m.opMode.query)
	m.opMode.matches = fuzzy.Filter(m.opMode.filteredName, m.opMode.corpus)
	m.opMode.index = input.Clamp(m.opMode.index, 0, max(0, len(m.opMode.matches)-1))
	return m
}

// setOpQuery updates the pick query. Only a change to the name part
// re-filters and resets the selection — typing args keeps the chosen row.
func (m Model) setOpQuery(query string) Model {
	m.opMode.query = query
	if name, _ := opQueryParts(query); name != m.opMode.filteredName {
		m.opMode.index = 0
		m = m.refreshOpMatches()
	}
	return m
}

// opHighlighted is the command the picker's selection points at.
func (m Model) opHighlighted() (store.OpCommand, bool) {
	if len(m.opMode.matches) == 0 {
		return store.OpCommand{}, false
	}
	index := input.Clamp(m.opMode.index, 0, len(m.opMode.matches)-1)
	return m.opMode.candidates[m.opMode.matches[index].Index], true
}

func (m Model) handleOpKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.opMode.stage {
	case opStageArgs:
		return m.handleOpArgsKey(msg)
	case opStageRun:
		return m.handleOpRunKey(msg)
	}
	switch msg.String() {
	case "esc", "ctrl+r":
		return m.closeOpMode(), nil
	case "up", "shift+up":
		m.opMode.index = input.Clamp(m.opMode.index-1, 0, max(0, len(m.opMode.matches)-1))
	case "down", "shift+down":
		m.opMode.index = input.Clamp(m.opMode.index+1, 0, max(0, len(m.opMode.matches)-1))
	case "enter":
		return m.selectOpCommand()
	case "backspace":
		if runes := []rune(m.opMode.query); len(runes) > 0 {
			m = m.setOpQuery(string(runes[:len(runes)-1]))
		}
	case "space":
		m = m.setOpQuery(m.opMode.query + " ")
	default:
		if text := keyText(msg); text != "" {
			m = m.setOpQuery(m.opMode.query + text)
		}
	}
	return m, nil
}

// selectOpCommand takes the highlighted command to the args stage, or runs it
// straight away when its template has no {$n} placeholders. Inline args typed
// after the name ("test 10") run it directly; if they don't fit the template
// the args stage opens pre-filled, showing why.
func (m Model) selectOpCommand() (tea.Model, tea.Cmd) {
	command, ok := m.opHighlighted()
	if !ok {
		return m, nil
	}
	template, err := opcmd.Parse(command.Command)
	if err != nil {
		m.errText = command.Name + ": " + err.Error()
		return m, nil
	}
	_, inlineArgs := opQueryParts(m.opMode.query)
	m.opMode.selected, m.opMode.template = command, template
	m.opMode.args, m.opMode.argsCursor = inlineArgs, len([]rune(inlineArgs))
	if inlineArgs != "" {
		if _, err := m.renderOpCommandLine(); err == nil {
			return m.runOpCommand()
		}
		m.opMode.stage = opStageArgs
		return m, nil
	}
	if template.MaxArg == 0 {
		return m.runOpCommand()
	}
	m.opMode.stage = opStageArgs
	return m, nil
}

// renderOpCommandLine builds the executable command line from the args input.
func (m Model) renderOpCommandLine() (string, error) {
	return m.renderOpTemplate(m.opMode.template, m.opMode.args)
}

func (m Model) renderOpTemplate(template opcmd.Template, argsLine string) (string, error) {
	args, err := opcmd.SplitArgs(argsLine)
	if err != nil {
		return "", err
	}
	return template.Render(args, m.opSystemValues())
}

// opInlinePreview renders the highlighted command with the pick query's
// inline args — exactly what Enter would run. ok is false without inline args.
func (m Model) opInlinePreview() (commandLine string, ok bool, err error) {
	_, inlineArgs := opQueryParts(m.opMode.query)
	command, found := m.opHighlighted()
	if inlineArgs == "" || !found {
		return "", false, nil
	}
	template, err := opcmd.Parse(command.Command)
	if err != nil {
		return "", true, err
	}
	commandLine, err = m.renderOpTemplate(template, inlineArgs)
	return commandLine, true, err
}

// opSystemValues resolves the system variables against the workspace
// directory, not the Ctrl+W repo root — commands run from the workspace, so
// the paths must be relative to it.
func (m Model) opSystemValues() map[string]string {
	if m.openFile == nil {
		return map[string]string{}
	}
	workspacePath := path.Join(m.activeRepo, m.openRel)
	return map[string]string{
		opcmd.SystemFilePath: workspacePath,
		opcmd.SystemDirPath:  path.Dir(workspacePath),
	}
}

func (m Model) handleOpArgsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.opMode.stage = opStagePick
	case "enter":
		return m.runOpCommand()
	case "left":
		m.opMode.argsCursor = input.MoveCursor(m.opMode.args, m.opMode.argsCursor, -1)
	case "right":
		m.opMode.argsCursor = input.MoveCursor(m.opMode.args, m.opMode.argsCursor, 1)
	case "home", "ctrl+a":
		m.opMode.argsCursor = 0
	case "end":
		m.opMode.argsCursor = len([]rune(m.opMode.args))
	case "backspace":
		m.opMode.args, m.opMode.argsCursor, _ = input.RemoveBeforeCursor(m.opMode.args, m.opMode.argsCursor)
	case "space":
		m.opMode.args, m.opMode.argsCursor = input.InsertAtCursor(m.opMode.args, m.opMode.argsCursor, " ")
	default:
		if text := keyText(msg); text != "" {
			m.opMode.args, m.opMode.argsCursor = input.InsertAtCursor(m.opMode.args, m.opMode.argsCursor, text)
		}
	}
	return m, nil
}

func (m Model) runOpCommand() (tea.Model, tea.Cmd) {
	commandLine, err := m.renderOpCommandLine()
	if err != nil {
		m.errText = err.Error()
		return m, nil
	}
	m.opRunGen++
	m.opMode.stage = opStageRun
	m.opMode.run = newOpRun(commandLine, m.workspaceRoot)
	return m, startOpRunCmd(m.opRunGen, m.opMode.run)
}

func (m Model) handleOpRunKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	run := m.opMode.run
	lineCount := len(run.output.displayLines())
	switch msg.String() {
	case "esc":
		return m.closeOpMode(), nil
	case "up":
		run.scroll = input.Clamp(run.scroll+1, 0, max(0, lineCount-1))
	case "down":
		run.scroll = input.Clamp(run.scroll-1, 0, max(0, lineCount-1))
	case "pgup":
		run.scroll = input.Clamp(run.scroll+opRunPageLines, 0, max(0, lineCount-1))
	case "pgdown":
		run.scroll = input.Clamp(run.scroll-opRunPageLines, 0, max(0, lineCount-1))
	case "home":
		run.scroll = max(0, lineCount-1)
	case "end":
		run.scroll = 0
	}
	return m, nil
}

// handleOpRunMsg appends streamed output and re-arms the wait until the
// process reports completion; messages from a closed or replaced run are
// dropped, which also ends their wait chain.
func (m Model) handleOpRunMsg(msg opRunMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.opRunGen || m.opMode.run == nil {
		return m, nil
	}
	run := m.opMode.run
	if len(msg.data) > 0 {
		before := len(run.output.lines)
		run.output.write(msg.data)
		if run.scroll > 0 {
			// Keep a scrolled-back view anchored on the same lines.
			run.scroll += max(0, len(run.output.lines)-before)
		}
	}
	if !msg.done {
		return m, waitOpRunCmd(msg.gen, run)
	}
	run.finished, run.exitCode, run.err = true, msg.exitCode, msg.err
	run.cancel() // releases the context; the process has already exited
	return m, nil
}

// opRunStatus is the run footer's text and whether it reports success.
func (run *opRunState) status() (string, bool) {
	switch {
	case !run.finished:
		return "running… Esc to cancel", true
	case run.err != nil:
		return "✗ " + strings.TrimSpace(run.err.Error()) + " — press Esc to close", false
	case run.exitCode != 0:
		return "✗ exited with status " + strconv.Itoa(run.exitCode) + " — press Esc to close", false
	default:
		return "✓ finished (exit 0) — press Esc to close", true
	}
}
